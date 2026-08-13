.PHONY: build-linux build-android clean

BINARY := opencode-daemon
CMD_DIR := ./cmd/daemon

build-linux:
	GOOS=linux GOARCH=amd64 go build -o $(BINARY) $(CMD_DIR)

build-android:
	GOOS=android GOARCH=arm64 go build -o $(BINARY)-android $(CMD_DIR)

clean:
	rm -f $(BINARY) $(BINARY)-android
