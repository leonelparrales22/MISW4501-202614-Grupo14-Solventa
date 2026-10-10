SERVICES := origination claims partners audit notifications

.PHONY: lint test build
lint:
	golangci-lint run ./pkg/... $(addprefix ./services/,$(addsuffix /...,$(SERVICES)))
test:
	go test -cover ./pkg/... $(addprefix ./services/,$(addsuffix /...,$(SERVICES)))
build:
	$(foreach s,$(SERVICES),go build -o bin/$(s) ./services/$(s)/cmd/$(s);)
