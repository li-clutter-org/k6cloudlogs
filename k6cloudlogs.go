/*
 *
 * k6cloudlogs - cloud logs for the next generation load generator k6
 * Copyright (C) 2020 Load Impact
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, either version 3 of the
 * License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

/*
{
  "streams": [
    {
      "stream": {
        <label key-value pairs>
      },
      "values": [
        [
          <string: nanosecond unix epoch>,
          <string: log line>
        ]
      ]
    }
  ],
  "dropped_entries": [
    {
      "labels": {
        <label key-value pairs>
      },
      "timestamp": "<nanosecond unix epoch>"
    }
  ]
}

*/

type msg struct {
	Streams []struct {
		Stream map[string]string `json:"stream"`
		Values [][2]string       `json:"values"` // this can be optimized
	} `json:"streams"`
	DroppedEntries []struct {
		Labels    map[string]interface{} `json:"labels"`
		Timestamp string                 `json:"timestamp"`
	} `json:"dropped_entries"`
}

func getLevelsStr(level string) ([]string, error) {
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("unknown log level %s", level) // specifically use a custom error
	}
	index := sort.Search(len(logrus.AllLevels), func(i int) bool {
		return logrus.AllLevels[i] > lvl
	})
	result := make([]string, index)
	for i, lvl := range logrus.AllLevels[:index] {
		result[i] = lvl.String()
	}

	return result, nil
}

func (m *msg) Log(logger logrus.FieldLogger) {
	var level string

	for _, stream := range m.Streams {
		fields := make(logrus.Fields, len(stream.Stream)-1)

		for key, val := range stream.Stream {
			if key == "level" {
				level = val
				continue
			}

			fields[key] = val
		}

		for _, value := range stream.Values {
			nsec, _ := strconv.Atoi(value[0])
			e := logger.WithFields(fields).WithTime(time.Unix(0, int64(nsec)))
			lvl, err := logrus.ParseLevel(level)
			if err != nil {
				e.Info(value[1])
				e.Warn("last message had unknown level " + level)
			} else {
				e.Log(lvl, value[1])
			}
		}
	}

	for _, dropped := range m.DroppedEntries {
		// nsec, _ := strconv.Atoi(dropped.Timestamp)
		nsec, _ := strconv.Atoi(dropped.Timestamp)
		logger.WithFields(
			logrus.Fields(dropped.Labels),
		).WithTime(time.Unix(0, int64(nsec))).Warn("dropped")
	}
}

func parseFilters(id, level string) ([]string, error) {
	idFilter := `test_run_id="` + id + `"`

	lvls, err := getLevelsStr(level)
	if err != nil {
		return nil, err
	}
	levelFilter := `level=~"(` + strings.Join(lvls, "|") + `)"`

	return []string{idFilter, levelFilter}, nil
}

//nolint:funlen
func main() {
	var (
		addr  = flag.String("addr", "wss://cloudlogs.k6.io/api/v1/tail", "loki address and path")
		id    = flag.String("id", "1232", "test run id")
		token = flag.String("token", "1232", "the token")
		level = flag.String("level", "info", "the info")
		start = flag.String("start", "5m", "from how long ago to start tailing")
		limit = flag.String("limit", "100", "how many messages should be in the ")
	)

	flag.Parse()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	defer signal.Stop(sigC)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-sigC
		cancel() // stop the test run, metric processing is cancelled below
		<-sigC
		os.Exit(1)
	}()

	u, err := url.Parse(*addr)
	if err != nil {
		log.Fatal(err)
	}

	filters, err := parseFilters(*id, *level)
	if err != nil {
		log.Fatal(err)
	}

	d, err := time.ParseDuration(*start)
	if err != nil {
		log.Fatal(fmt.Errorf("invalid duration %s, error parsing: %w", *start, err))
	}

	l, err := strconv.Atoi(*limit)
	if err != nil {
		log.Fatal(fmt.Errorf("invalid limit %s, error parsing: %w", *limit, err))
	}

	u.RawQuery = fmt.Sprintf(`query={%s}&limit=%d&start=%d`, strings.Join(filters, ","), l, time.Now().Add(-d).UnixNano())

	log.Printf("connecting to %s", u.String())

	headers := make(http.Header)
	headers.Add("Sec-WebSocket-Protocol", "token="+*token)

	c, h, err := websocket.DefaultDialer.DialContext(ctx, u.String(), headers)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer func() {
		_ = h.Body.Close()
		_ = c.Close()
	}()

	log.Printf("connected")

	go func() {
		<-ctx.Done()

		_ = c.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "closing"),
			time.Now().Add(time.Second))

		_ = c.Close()
	}()

	logger := logrus.New()
	logger.Level = logrus.TraceLevel
	logger.Formatter = &logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02T15:04:05.0000",
	}

	msgBuffer := make(chan []byte, 10)

	defer close(msgBuffer)

	go func() {
		for message := range msgBuffer {
			var m msg
			err = json.Unmarshal(message, &m)

			if err != nil {
				logger.WithError(err).Error("couldn't unmarshal:" + string(message))
			}

			m.Log(logger)
		}
	}()

	for {
		_, message, err := c.ReadMessage()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			log.Println("read:", err)

			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}

		select {
		case <-ctx.Done():
			return
		case msgBuffer <- message:
		}
	}
}
