test:
	@go test ./...

fmt:
	goimports -w -local github.com/DBN-DEV/skyeye ./.

gen-pb:
	@echo Build proto file
	@protoc --go-grpc_out=./pb --go_out=./pb ./proto/*.proto
	@echo Build proto file done
