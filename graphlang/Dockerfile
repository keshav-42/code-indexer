# Explicitly set builder platform
FROM --platform=linux/amd64 golang:1.21 AS builder

WORKDIR /analyzer
COPY . .
RUN go mod download

# Build for linux/amd64
RUN GOOS=linux GOARCH=amd64 go build -o graphlang-analyzer .

# Explicitly set final stage platform to match the build
FROM --platform=linux/amd64 alpine:3.18

WORKDIR /analyzer
RUN apk add --no-cache libc6-compat libgcc
COPY --from=builder /analyzer/graphlang-analyzer /analyzer/
RUN chmod +x graphlang-analyzer
CMD ["./graphlang-analyzer"]