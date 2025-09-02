.PHONY: generate clean

generate:
	@echo "Generating client code from OpenAPI specification..."
	go generate ./...
	@echo "Client code generation complete!"

clean:
	@echo "Cleaning generated files..."
	rm -f generated.go
	@echo "Cleaned generated files!"

install-deps:
	@echo "Installing oapi-codegen..."
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
	@echo "Dependencies installed!"
