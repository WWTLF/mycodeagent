APP_NAME := mycodeagent
CMD := ./cmd/$(APP_NAME)

.PHONY: build run test clean

build:
	go build -o $(APP_NAME) $(CMD)

run:
	go run $(CMD)

test:
	go test ./...

clean:
	rm -f $(APP_NAME)
