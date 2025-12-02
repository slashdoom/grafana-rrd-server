FROM golang:alpine AS builder

RUN apk update && apk add --no-cache pkgconfig rrdtool-dev gcc libc-dev

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o grafana-rrd-server .

FROM alpine:latest
RUN apk --no-cache add rrdtool rrdtool-dev ca-certificates
COPY --from=builder /build/grafana-rrd-server /grafana-rrd-server
EXPOSE 9000
ENTRYPOINT [ "/grafana-rrd-server" ]