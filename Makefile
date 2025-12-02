.PHONY: test run build clean tidy

test:
	go test -v -parallel=4 .

run:
	go run rrdserver.go

build:
	go build -o grafana-rrd-server .

clean:
	rm -f grafana-rrd-server

tidy:
	go mod tidy
