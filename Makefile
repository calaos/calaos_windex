
APPNAME = calaos_windex

TAGS = ""
BUILD_FLAGS = "-v"

.PHONY: build clean

build: $(GENERATED)
	go install $(BUILD_FLAGS) -ldflags '$(LDFLAGS)' -tags '$(TAGS)'
	cp '$(GOPATH)/bin/$(APPNAME)' .

clean:
	go clean -i ./...


