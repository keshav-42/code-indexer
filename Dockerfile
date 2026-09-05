# Base stage
FROM golang:1.21

RUN apt-get update && apt-get install -y libstdc++6 libgcc1 && rm -rf /var/lib/apt/lists/*

WORKDIR /analyzer
COPY go.mod go.sum main.go ./
COPY graphlang ./graphlang
RUN go mod download \
    && go get -u github.com/smacker/go-tree-sitter \
    && go get -u github.com/neo4j/neo4j-go-driver/v5/neo4j

RUN go build main.go

ENV PATH="/root/go/bin:${PATH}"

ENTRYPOINT ["/analyzer/main"]
