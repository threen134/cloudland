export GO111MODULE=on

all: build

version:
	git describe --always --tags > .version 2>/dev/null || echo "dev" > .version

build: version
	go build -o cpgateway-go -ldflags "-X \"main.Version=$$(cat .version)\""

test:
	go test ./...

tidy:
	go mod tidy

clean:
	rm -f cpgateway-go .version

.PHONY: all version build test tidy clean
