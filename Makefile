.PHONY: build run

build:
	go build -o dist/harp .

run: build
	./dist/harp $(ARGS)
