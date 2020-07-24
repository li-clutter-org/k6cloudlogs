FROM golang:1.14-alpine as builder

RUN apk --no-cache add git
WORKDIR /6kcloudlogs
ADD . .
RUN CGO_ENABLED=0 go build -v -o "/bin/k6cloudlogs" -a -trimpath

FROM alpine:latest
RUN adduser -D -u 12345 -g 12345 k6cloudlogs
WORKDIR /k6cloudlogs

COPY --from=builder /bin/k6cloudlogs /bin/k6cloudlogs

ENTRYPOINT ["/bin/k6cloudlogs"]
