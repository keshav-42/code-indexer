# Code Analysis Tool with Neo4j

Indexed codebase structure
![image](https://github.com/user-attachments/assets/7dedb4c8-15e0-45fc-8928-dacbef5f1d76)

This project provides a multi-language code analysis tool that analyzes source code and creates a graph representation in Neo4j. It currently supports the following languages:

- Go
- Python
- JavaScript/TypeScript
- And many others through the Tree-sitter parser

## Prerequisites

- Docker and Docker Compose
- Neo4j (handled via Docker)
- Source code to analyze

## Setup

1. Clone the repository
2. Copy the environment file template:
   ```bash
   cp .env.example .env
   ```
3. Update the `.env` file with your configuration:
   ```ini
   NEO4J_URI=bolt://host.docker.internal:7687
   NEO4J_USER=neo4j
   NEO4J_PASSWORD=yourpassword
   ```

## Running the Analysis

1. Start the Neo4j instance and analyzers:

   ```bash
   docker-compose up -d
   ```

2. Access Neo4j Browser at `http://localhost:7474` to view the results

## Analyzing Different Code Bases

To analyze your own code, modify the `docker-compose.yml` file. Each analyzer service has a volume mount that specifies which directory to analyze:

### For Go Code

```yaml
go-analyzer:
  volumes:
    - /path/to/your/go/code:/app
  environment:
    - ROOT_NAME=your-project-name <- add path of the codebase you want to scan here
    - BASE_URL=http://localhost
```

### For Python Code

```yaml
python-analyzer:
  volumes:
    - /path/to/your/python/code:/app
  environment:
    - ROOT_NAME=your-python-project <- add path of the codebase you want to scan here
    - BASE_URL=http://localhost
```

### For JavaScript/TypeScript Code

```yaml
js-analyzer:
  volumes:
    - /path/to/your/js/code:/app
  environment:
    - ROOT_NAME=your-js-project <- add path of the codebase you want to scan here
    - BASE_URL=http://localhost
```

### For Other Languages (using Tree-sitter)

```yaml
graphlang-analyzer:
  volumes:
    - /path/to/your/code:/app
  environment:
    - ROOT_NAME=your-project <- add path of the codebase you want to scan here
    - BASE_URL=http://localhost
```

## Supported Languages by Tree-sitter Parser

The graphlang analyzer supports multiple languages including:

- Go
- JavaScript/TypeScript
- Python
- Java
- C/C++
- C#
- Ruby
- Rust
- PHP
- Swift
- Kotlin
- And more...

## Graph Structure

The analysis creates a graph with the following node types:

- Root
- Package/Namespace
- Class/Struct
- Function
- Method
- Interface

Relationships between nodes include:

- CONTAINS
- DECLARES
- CALLS
- HAS_MEMBER

## Viewing Results

1. Open Neo4j Browser at `http://localhost:7474`
2. Login with your credentials
3. Run Cypher queries to explore the code structure, for example:
   ```cypher
   MATCH (n) RETURN n LIMIT 100
   ```
   ```cypher
   MATCH p=(:Function)-[:CALLS]->() RETURN p LIMIT 50
   ```

## Troubleshooting

1. If Neo4j fails to start:

   ```bash
   docker-compose restart neo4j
   ```

2. If analyzers can't connect to Neo4j:

   - Ensure Neo4j is running: `docker-compose ps`
   - Check logs: `docker-compose logs -f neo4j`
   - Verify credentials in `.env` file

3. For permission issues with mounted volumes:
   - Ensure proper read permissions on source code directories
   - Run Docker with appropriate user permissions

## Note

The analysis results are stored in Neo4j. Each new analysis run will clean up previous results for the same project name (ROOT_NAME) to ensure fresh analysis.
