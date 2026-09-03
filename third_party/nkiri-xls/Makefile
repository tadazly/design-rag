.PHONY: test cov

test:
	go test ./...

cov:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
