APPNAME = calaos_windex

TAGS = ""
BUILD_FLAGS = "-v"
LDFLAGS = "-extldflags=-static"

.PHONY: build clean

build:
	CGO_ENABLED=0 go build $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -tags '$(TAGS)' -o $(APPNAME) .

clean:
	go clean -i ./...


