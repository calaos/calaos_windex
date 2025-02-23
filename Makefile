APPNAME = calaos_windex

TAGS = ""
BUILD_FLAGS = "-v"
LDFLAGS = "-extldflags=-static"

.PHONY: build clean

build:
	CGO_ENABLED=0 go install $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -tags '$(TAGS)'
	cp '$(GOPATH)/bin/$(APPNAME)' .

clean:
	go clean -i ./...


