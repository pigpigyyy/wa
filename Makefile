# 版权 @2019 凹语言 作者。保留所有权利。

.PHONY: wa hello prime build-wasm ci-test-all clean

GOBIN = ./build/bin/wa
DOCKER_VOLUME=-v $(shell pwd):/root

wa:
	go build -o $(GOBIN)
	@echo "Done building."
	@echo "Run \"$(GOBIN)\" to launch wa."

hello:
	go install
	cd waroot && go run ../main.go run hello.wa

dev:
	go run main.go p9asm -S ./tests/p9asm/hello.s

dev-nm:
	go run main.go p9nm ./tests/p9asm/hello.o

dev-link:
	go run main.go p9link -o a.out ./tests/p9asm/hello.o

prime:
	cd waroot && go run ../main.go run examples/prime

build-wasm:
	GOARCH=wasm GOOS=js go build -o wa.out.wasm ./main_wasm.go

build-docker:
	go run ./builder
	docker build -t wa-lang/wa .

docker-run:
	docker run --platform linux/amd64 --rm -it ${DOCKER_VOLUME} wa-lang/wa

ci-test-all:
	go install
	go test ./...

	@echo "== std test begin =="
	go run main.go test std
	@echo "== std ok =="

	go run main.go run ./waroot/hello.wa
	cd waroot && go run ../main.go run hello.wa

	make -C ./waroot/examples ci-test-all

	@echo "== nil check test begin =="
	go run main.go test ./waroot/tests/nil-check/nil_pointer_deref_test.wa
	@echo "== nil check test ok =="

	wa -v

wasm-js:
	-@rm ./wa.wasm
	@mkdir -p ./docs/wa-js
	GOOS=js GOARCH=wasm go build -o wa.wasm
	mv wa.wasm ./docs/wa-js
	cd ./docs/wa-js && zip wa.wasm.zip wa.wasm

wasm-wasip1:
	-@rm ./wa.wasm
	@mkdir -p ./docs/wa-wasip1
	GOOS=wasip1 GOARCH=wasm go build -o wa.wasm
	mv wa.wasm ./docs/wa-wasip1
	cd ./docs/wa-wasip1 && zip wa.wasm.zip wa.wasm

clean:
	-rm a.out*

GO_IMAGE := golang:1.24

build-x86:
	docker run --rm \
	  --platform linux/amd64 \
	  -v $(PWD):/go/src/app \
	  -w /go/src/app \
	  -e CGO_ENABLED=1 \
	  -e GOOS=linux \
	  -e GOARCH=amd64 \
	  $(GO_IMAGE) \
	  sh -c "go build -buildmode=c-archive -ldflags='-s -w' -o libwa.a"
	cp libwa.a ../Dora-SSR/Source/3rdParty/Wa/Lib/Linux/amd64/
	rm -f libwa.a libwa.h

