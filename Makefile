# 本地门禁：与 .github/workflows/ci.yml 的 Test / Build 保持一致
# 用法：make check（CI 会跑同一套命令）
GO   ?= go
PKGS := ./...

.PHONY: check fmt fix vet build test cross

check: fmt vet build test cross

fmt:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then \
		echo "以下文件未格式化，运行 make fix 修复："; \
		echo "$$out"; \
		exit 1; \
	fi

fix:
	gofmt -w .

vet:
	$(GO) vet $(PKGS)

build:
	$(GO) build $(PKGS)

test:
	$(GO) test -race -shuffle=on -count=1 -covermode=atomic -coverprofile=coverage.out $(PKGS)

cross:
	GOOS=darwin  GOARCH=arm64 $(GO) build $(PKGS)
	GOOS=windows GOARCH=amd64 $(GO) build $(PKGS)
