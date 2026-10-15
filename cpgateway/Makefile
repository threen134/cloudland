export GO111MODULE=on

all: build

version:
	cp -f ../VERSION .version

build: version
	go build -o cpgateway -ldflags "-X \"main.Version=$$(cat .version)\""

test:
	go test ./...

tidy:
	go mod tidy

clean:
	rm -f cpgateway .version

.PHONY: all version build test tidy clean
