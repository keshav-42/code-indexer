# Use an official Golang image as a parent image
FROM golang:1.23 AS builder

# Set the working directory inside the container
WORKDIR /analyzer

# Copy the Go module files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code into the container
COPY . .

# Build the Go binary
RUN go build -o code-analyzer .

# Use a minimal alpine image for the final container
FROM alpine:3.18

# Set the working directory inside the container
WORKDIR /analyzer

# Install necessary runtime dependencies
RUN apk add --no-cache libc6-compat

# Copy the built binary from the builder stage
COPY --from=builder /analyzer/code-analyzer /analyzer/

# Set environment variables for Neo4j (adjust as needed)
ENV NEO4J_URI=bolt://host.docker.internal:7687
ENV NEO4J_USER=neo4j
ENV NEO4J_PASSWORD=securepassword

# Command to run the Go binary
CMD ["./code-analyzer"]
