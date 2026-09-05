package graphlang

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/smacker/go-tree-sitter/bash"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	"github.com/smacker/go-tree-sitter/css"
	"github.com/smacker/go-tree-sitter/cue"
	"github.com/smacker/go-tree-sitter/dockerfile"
	"github.com/smacker/go-tree-sitter/elixir"
	"github.com/smacker/go-tree-sitter/elm"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/groovy"
	"github.com/smacker/go-tree-sitter/hcl"
	"github.com/smacker/go-tree-sitter/html"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/kotlin"
	"github.com/smacker/go-tree-sitter/lua"
	markdown "github.com/smacker/go-tree-sitter/markdown/tree-sitter-markdown"
	"github.com/smacker/go-tree-sitter/ocaml"
	"github.com/smacker/go-tree-sitter/php"
	"github.com/smacker/go-tree-sitter/protobuf"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/scala"
	"github.com/smacker/go-tree-sitter/sql"
	"github.com/smacker/go-tree-sitter/svelte"
	"github.com/smacker/go-tree-sitter/swift"
	"github.com/smacker/go-tree-sitter/toml"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
	"github.com/smacker/go-tree-sitter/yaml"
)

// CallRelation struct to store pending function call relationships
type CallRelation struct {
	CallerName string
	CalleeName string
	Arguments  []string
}

// TreeSitterParser struct
type TreeSitterParser struct {
	Handle           *sitter.Parser
	language         *sitter.Language
	code             string
	ext              string
	currentFunc      string
	currentNamespace string
	currentType      string
	driver           neo4j.DriverWithContext
	tree             *sitter.Tree

	// Pending call relationships
	pendingRelations []CallRelation
	pendingMutex     sync.Mutex
}

// NewTreeSitterParser initializes the TreeSitterParser and connects it to Neo4j.
func NewTreeSitterParser(driver neo4j.DriverWithContext) *TreeSitterParser {
	return &TreeSitterParser{
		Handle: sitter.NewParser(),
		driver: driver,
	}
}

// ExtToLang sets the parser's language based on the file extension.
func (parser *TreeSitterParser) ExtToLang(ext string) (*sitter.Language, error) {
	switch ext {
	case "go":
		return golang.GetLanguage(), nil
	case "js":
		return javascript.GetLanguage(), nil
	case "py":
		return python.GetLanguage(), nil
	case "c":
		return c.GetLanguage(), nil
	case "cpp":
		return cpp.GetLanguage(), nil
	case "sh":
		return bash.GetLanguage(), nil
	case "rb":
		return ruby.GetLanguage(), nil
	case "rs":
		return rust.GetLanguage(), nil
	case "java":
		return java.GetLanguage(), nil
	case "ts":
		return typescript.GetLanguage(), nil
	case "tsx":
		return tsx.GetLanguage(), nil
	case "php":
		return php.GetLanguage(), nil
	case "cs":
		return csharp.GetLanguage(), nil
	case "css":
		return css.GetLanguage(), nil
	case "dockerfile":
		return dockerfile.GetLanguage(), nil
	case "ex", "exs":
		return elixir.GetLanguage(), nil
	case "elm":
		return elm.GetLanguage(), nil
	case "hcl":
		return hcl.GetLanguage(), nil
	case "html":
		return html.GetLanguage(), nil
	case "kt":
		return kotlin.GetLanguage(), nil
	case "lua":
		return lua.GetLanguage(), nil
	case "ml", "mli":
		return ocaml.GetLanguage(), nil
	case "scala":
		return scala.GetLanguage(), nil
	case "sql":
		return sql.GetLanguage(), nil
	case "swift":
		return swift.GetLanguage(), nil
	case "toml":
		return toml.GetLanguage(), nil
	case "yaml", "yml":
		return yaml.GetLanguage(), nil
	case "cue":
		return cue.GetLanguage(), nil
	case "groovy":
		return groovy.GetLanguage(), nil
	case "md", "markdown":
		return markdown.GetLanguage(), nil
	case "proto", "protobuf":
		return protobuf.GetLanguage(), nil
	case "svelte":
		return svelte.GetLanguage(), nil
	default:
		return nil, fmt.Errorf("unsupported language: %s", ext)
	}
}

// Parse generates the syntax tree for the code.
func (parser *TreeSitterParser) Parse() error {
	language, err := parser.ExtToLang(parser.ext)
	if err != nil {
		return fmt.Errorf("failed to get language: %v", err)
	}
	parser.language = language
	parser.Handle.SetLanguage(parser.language)
	tree := parser.Handle.Parse(nil, []byte(parser.code))
	if tree == nil {
		return fmt.Errorf("failed to parse code: tree is nil")
	}

	parser.tree = tree
	return nil
}

// Analyze processes the syntax tree to extract functions and call paths.
func (parser *TreeSitterParser) Analyze() error {
	if parser.tree == nil {
		return fmt.Errorf("no syntax tree available, call Parse() first")
	}

	session := parser.driver.NewSession(context.Background(), neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(context.Background())

	// Fix: Capture both return values and use proper type
	result, err := session.ExecuteWrite(context.Background(),
		func(tx neo4j.ManagedTransaction) (interface{}, error) {
			rootNode := parser.tree.RootNode()
			return nil, parser.traverseNode(rootNode, tx)
		})
	if err != nil {
		return fmt.Errorf("failed to execute transaction: %v", err)
	}
	_ = result // Ignore the result if not needed

	return nil
}

// traverseNode recursively traverses the AST and handles different node types.
func (parser *TreeSitterParser) traverseNode(node *sitter.Node, tx neo4j.ManagedTransaction) error {
	if node == nil {
		return nil
	}

	// Save current context
	prevNamespace := parser.currentNamespace
	prevType := parser.currentType
	prevFunc := parser.currentFunc

	var processErr error
	switch node.Type() {
	case "namespace_definition", "package_declaration", "namespace":
		processErr = parser.handleNamespace(node, tx)
	case "class_declaration", "struct_declaration", "interface_declaration", "class", "struct", "interface":
		processErr = parser.handleType(node, tx)
	case "function_declaration", "method_declaration", "function", "method", "arrow_function", "anonymous_function", "function_expression":
		processErr = parser.handleFunction(node, tx)
	case "member_expression":
		processErr = parser.handleMemberExpression(node, tx)
	case "call_expression", "function_call_expression":
		processErr = parser.handleCallExpression(node, tx)
	case "argument", "arguments", "argument_list", "formal_parameters":
		processErr = parser.handleArguments(node, tx)
	// Handle additional node types
	case "using_directive", "attribute_argument", "class_name", "qualified_name":
		// These node types might not affect function context directly.
		// Implement handlers if necessary or skip.
		processErr = nil // No action needed currently
	case "block_mapping", "block_mapping_pair", "flow_node", "string_scalar", "plain_scalar", "declaration", "property_name", "string_literal":
		// These might be part of data structures or literals.
		// Implement generic handling or skip.
		processErr = nil // No action needed currently
	default:
		// Log unhandled node types for further investigation
		// log.Printf("Info: Unhandled node type '%s' at position %d:%d",
		// 	node.Type(), node.StartPoint().Row, node.StartPoint().Column)
	}

	if processErr != nil {
		return processErr
	}

	// Traverse children
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child != nil {
			err := parser.traverseNode(child, tx)
			if err != nil {
				return err
			}
		}
	}

	// Restore previous context
	parser.currentNamespace = prevNamespace
	parser.currentType = prevType
	parser.currentFunc = prevFunc

	return nil
}

// cleanup handles database cleanup
func (parser *TreeSitterParser) cleanup() error {
	neo4jURI := os.Getenv("NEO4J_URI")
	if neo4jURI == "" {
		neo4jURI = "bolt://neo4j:7687"
	}
	neo4jUser := os.Getenv("NEO4J_USER")
	if neo4jUser == "" {
		neo4jUser = "neo4j"
	}
	neo4jPassword := os.Getenv("NEO4J_PASSWORD")
	if neo4jPassword == "" {
		neo4jPassword = "myneo4jpass"
	}

	log.Printf("Connecting to Neo4j at %s with user %s\n", neo4jURI, neo4jUser)
	driver, err := neo4j.NewDriverWithContext(neo4jURI, neo4j.BasicAuth(neo4jUser, neo4jPassword, ""))
	if err != nil {
		log.Fatalf("Failed to create Neo4j driver: %v", err)
	}
	defer driver.Close(context.Background())

	session := driver.NewSession(context.Background(), neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(context.Background())

	// First transaction: Delete all nodes and relationships
	log.Printf("Starting data cleanup...")
	tx1, err := session.BeginTransaction(context.Background())
	if err != nil {
		return fmt.Errorf("failed to create data cleanup transaction: %v", err)
	}

	_, err = tx1.Run(
		context.Background(),
		"MATCH (n) DETACH DELETE n",
		map[string]any{},
	)
	if err != nil {
		_ = tx1.Rollback(context.Background())
		return fmt.Errorf("failed to delete data: %v", err)
	}

	if err := tx1.Commit(context.Background()); err != nil {
		return fmt.Errorf("failed to commit data cleanup: %v", err)
	}
	log.Printf("Data cleanup completed")

	// Second transaction: Schema operations
	log.Printf("Starting schema setup...")
	tx2, err := session.BeginTransaction(context.Background())
	if err != nil {
		return fmt.Errorf("failed to create schema transaction: %v", err)
	}

	schemaQueries := []string{
		"CREATE INDEX IF NOT EXISTS FOR (n:Namespace) ON (n.id)",
		"CREATE INDEX IF NOT EXISTS FOR (t:Type) ON (t.id)",
		"CREATE INDEX IF NOT EXISTS FOR (f:Function) ON (f.id)",
	}

	for _, query := range schemaQueries {
		if _, err := tx2.Run(
			context.Background(),
			query,
			map[string]any{},
		); err != nil {
			_ = tx2.Rollback(context.Background())
			log.Printf("Warning: Schema operation failed: %v", err)
			break
		}
	}

	if err := tx2.Commit(context.Background()); err != nil {
		log.Printf("Warning: Failed to commit schema changes: %v", err)
	}

	return nil
}

// AnalyzeDirectory walks through a directory and analyzes all supported files.
func (parser *TreeSitterParser) AnalyzeDirectory(dirPath string) error {
	// Attempt cleanup but continue even if it fails
	log.Printf("Starting database cleanup...")
	if err := parser.cleanup(); err != nil {
		log.Printf("Warning: Database cleanup failed: %v", err)
		log.Printf("Continuing with analysis...")
	} else {
		log.Printf("Database cleanup completed successfully")
	}

	// Initialize analysis state
	var (
		processedFiles atomic.Int32
		totalFiles     atomic.Int32
		wg             sync.WaitGroup
		fileChan       = make(chan string, 100)
	)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Start progress reporting
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	go func() {
		for {
			select {
			case <-ticker.C:
				processed := processedFiles.Load()
				total := totalFiles.Load()
				if total > 0 {
					progress := float64(processed) / float64(total) * 100
					log.Printf("Analysis progress: %.2f%% (%d/%d files)",
						progress, processed, total)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Count total files first
	if err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && parser.isSupportedFile(path) {
			totalFiles.Add(1)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to count files: %v", err)
	}

	log.Printf("Found %d files to analyze", totalFiles.Load())

	// Start worker pool
	numWorkers := runtime.NumCPU()
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for {
				select {
				case path, ok := <-fileChan:
					if !ok {
						return
					}
					if err := parser.processFile(ctx, path); err != nil {
						log.Printf("Worker %d: Error processing %s: %v",
							workerID, path, err)
						continue // Continue with next file even if one fails
					}
					processedFiles.Add(1)
				case <-ctx.Done():
					return
				}
			}
		}(i)
	}

	// Feed files to workers
	go func() {
		defer close(fileChan)
		_ = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				log.Printf("Warning: Error accessing path %s: %v", path, err)
				return nil // Continue walking even if there's an error
			}

			if info.IsDir() {
				switch info.Name() {
				case "node_modules", ".git", "vendor", "build", "dist":
					return filepath.SkipDir
				}
				return nil
			}

			if parser.isSupportedFile(path) {
				select {
				case fileChan <- path:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	}()

	// Wait for all workers to finish
	wg.Wait()

	processed := processedFiles.Load()
	total := totalFiles.Load()
	log.Printf("Initial analysis completed: Processed %d/%d files (%.2f%%)",
		processed, total, float64(processed)/float64(total)*100)

	// Process pending CALLS relationships
	if _, err := parser.processPendingRelations(); err != nil {
		log.Printf("Error processing pending CALLS relationships: %v", err)
	}

	processed = processedFiles.Load()
	log.Printf("Analysis completed: Processed %d/%d files (%.2f%%)",
		processed, total, float64(processed)/float64(total)*100)

	return nil
}

// processPendingRelations processes all pending CALLS relationships.
func (parser *TreeSitterParser) processPendingRelations() (neo4j.ResultWithContext, error) {
	parser.pendingMutex.Lock()
	pending := parser.pendingRelations
	parser.pendingRelations = nil
	parser.pendingMutex.Unlock()

	if len(pending) == 0 {
		log.Printf("No pending CALLS relationships to process.")
		return nil, nil
	}

	log.Printf("Processing %d pending CALLS relationships...", len(pending))

	// Create a new session for processing pending relations
	session := parser.driver.NewSession(context.Background(), neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(context.Background())

	for _, rel := range pending {
		_, err := session.ExecuteWrite(context.Background(),
			func(tx neo4j.ManagedTransaction) (interface{}, error) {
				return tx.Run(
					context.Background(),
					`MATCH (caller:Function {qualifiedName: $callerName}), 
						   (callee:Function {qualifiedName: $calleeName})
					 MERGE (caller)-[c:CALLS]->(callee)
					 ON CREATE SET 
						 c.arguments = $args,
						 c.timestamp = timestamp()`,
					map[string]any{
						"callerName": rel.CallerName,
						"calleeName": rel.CalleeName,
						"args":       rel.Arguments,
					},
				)
			})
		if err != nil {
			log.Printf("Failed to create pending CALLS relationship from '%s' to '%s': %v",
				rel.CallerName, rel.CalleeName, err)
			// Optionally, you can re-add to pendingRelations or handle retries here
		} else {
			log.Printf("Successfully created pending CALLS relationship from '%s' to '%s'",
				rel.CallerName, rel.CalleeName)
		}
	}

	return nil, nil
}

// processFile with proper session management
func (parser *TreeSitterParser) processFile(ctx context.Context, path string) error {
	// Read file
	code, err := parser.readFile(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to read file: %v", err)
	}

	// Create a new parser instance for this file
	fileParser := &TreeSitterParser{
		Handle: sitter.NewParser(),
		driver: parser.driver,
		code:   string(code),
		ext:    filepath.Ext(path)[1:], // This gets the extension without the dot
	}

	// Parse and analyze the file
	if err := fileParser.Parse(); err != nil {
		return fmt.Errorf("failed to parse file: %v", err)
	}
	if err := fileParser.Analyze(); err != nil {
		return fmt.Errorf("failed to analyze file: %v", err)
	}

	return nil
}

// readFile reads a file with context awareness
func (parser *TreeSitterParser) readFile(ctx context.Context, path string) ([]byte, error) {
	// Create a channel for the read result
	resultChan := make(chan struct {
		data []byte
		err  error
	})

	// Read file in a goroutine
	go func() {
		data, err := ioutil.ReadFile(path)
		resultChan <- struct {
			data []byte
			err  error
		}{data, err}
	}()

	// Wait for either the read to complete or context to be cancelled
	select {
	case result := <-resultChan:
		return result.data, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// isSupportedFile checks if the file extension matches supported languages.
func (parser *TreeSitterParser) isSupportedFile(filename string) bool {
	supportedExtensions := []string{
		"go", "js", "py", "c", "cpp", "sh", "rb", "rs", "java",
		"ts", "tsx", "php", "cs", "css", "dockerfile", "ex", "exs",
		"elm", "hcl", "html", "kt", "lua", "ml", "mli", "scala",
		"sql", "swift", "toml", "yaml", "yml", "cue", "groovy", "md", "markdown", "proto", "protobuf", "svelte",
	}
	ext := filepath.Ext(filename)
	if len(ext) > 0 {
		ext = ext[1:] // remove dot
	}
	for _, supportedExt := range supportedExtensions {
		if ext == supportedExt {
			return true
		}
	}
	return false
}

// handleFunction processes function declarations and sets the current function context.
func (parser *TreeSitterParser) handleFunction(node *sitter.Node, tx neo4j.ManagedTransaction) error {
	nameNode := node.ChildByFieldName("name")
	var funcName string

	if nameNode != nil {
		funcName = nameNode.Content([]byte(parser.code))
	} else {
		// Handle anonymous functions by generating a unique name based on position
		position := node.StartPoint()
		funcName = fmt.Sprintf("anonymous_func_%d_%d", position.Row, position.Column)
	}

	position := node.StartPoint()

	// Build fully qualified name
	qualifiedName := funcName
	if parser.currentType != "" {
		qualifiedName = fmt.Sprintf("%s.%s", parser.currentType, funcName)
	} else if parser.currentNamespace != "" {
		qualifiedName = fmt.Sprintf("%s.%s", parser.currentNamespace, funcName)
	}

	// Extract function metadata
	params := parser.extractParameters(node)
	returnType := parser.extractReturnType(node)

	_, err := tx.Run(
		context.Background(),
		`MERGE (f:Function {id: $id})
		 ON CREATE SET 
			 f.name = $name,
			 f.qualifiedName = $qualifiedName,
			 f.file = $file,
			 f.position = $position,
			 f.parameters = $params,
			 f.returnType = $returnType
		 WITH f
		 OPTIONAL MATCH (t:Type {qualifiedName: $typeName})
		 WHERE $typeName IS NOT NULL
		 MERGE (t)-[:HAS_MEMBER]->(f)`,
		map[string]any{
			"id":            fmt.Sprintf("%s:%s", parser.ext, qualifiedName),
			"name":          funcName,
			"qualifiedName": qualifiedName,
			"file":          parser.ext,
			"position":      fmt.Sprintf("%d:%d", position.Row, position.Column),
			"params":        params,
			"returnType":    returnType,
			"typeName":      parser.currentType,
		},
	)

	if err == nil {
		parser.currentFunc = qualifiedName
		log.Printf("Info: Set currentFunc to '%s'", qualifiedName)
	} else {
		log.Printf("Error handling function '%s': %v", qualifiedName, err)
	}
	return err
}

// handleArguments processes arguments and handles global scope calls.
func (parser *TreeSitterParser) handleArguments(node *sitter.Node, tx neo4j.ManagedTransaction) error {
	// Currently, no action is required.
	// Future enhancements can be added here if needed.
	return nil
}

// handleMemberExpression processes member expressions and handles global scope calls.
func (parser *TreeSitterParser) handleMemberExpression(node *sitter.Node, tx neo4j.ManagedTransaction) error {
	// Currently, no action is required.
	// Future enhancements can be added here if needed.
	return nil
}

// handleCallExpression processes call expressions and handles global scope calls.
func (parser *TreeSitterParser) handleCallExpression(node *sitter.Node, tx neo4j.ManagedTransaction) error {
	if parser.currentFunc == "" {
		log.Printf("Info: Found call_expression in global scope. Skipping or handling differently for callee: %s at position %d:%d",
			node.Content([]byte(parser.code)),
			node.StartPoint().Row,
			node.StartPoint().Column)
		return nil
	}

	calleeNode := node.ChildByFieldName("function")
	if calleeNode == nil {
		log.Printf("Warning: callee node not found in call_expression at position %d:%d",
			node.StartPoint().Row, node.StartPoint().Column)
		return nil
	}

	calleeName := calleeNode.Content([]byte(parser.code))
	args := parser.extractArguments(node)

	// Resolve the fully qualified callee name
	qualifiedCallee := parser.resolveQualifiedName(calleeName)

	if qualifiedCallee == "" {
		log.Printf("Warning: qualifiedCallee is empty for calleeName: %s at position %d:%d",
			calleeName,
			node.StartPoint().Row,
			node.StartPoint().Column)
		return nil
	}

	// Check if the callee function exists
	exists, err := parser.checkFunctionExists(tx, qualifiedCallee)
	if err != nil {
		log.Printf("Error checking existence of callee '%s': %v", qualifiedCallee, err)
		return err
	}

	if !exists {
		// Add to pending relations
		parser.addPendingRelation(CallRelation{
			CallerName: parser.currentFunc,
			CalleeName: qualifiedCallee,
			Arguments:  args,
		})
		log.Printf("Info: Added pending CALLS relationship from '%s' to '%s'",
			parser.currentFunc, qualifiedCallee)
		return nil
	}

	// Create the CALLS relationship
	_, err = tx.Run(
		context.Background(),
		`MATCH (caller:Function {qualifiedName: $callerName}), 
			   (callee:Function {qualifiedName: $calleeName})
		 MERGE (caller)-[c:CALLS]->(callee)
		 ON CREATE SET 
			 c.arguments = $args,
			 c.timestamp = timestamp()`,
		map[string]any{
			"callerName": parser.currentFunc,
			"calleeName": qualifiedCallee,
			"args":       args,
		},
	)
	if err != nil {
		log.Printf("Error creating CALLS relationship from '%s' to '%s' at position %d:%d: %v",
			parser.currentFunc, qualifiedCallee,
			node.StartPoint().Row,
			node.StartPoint().Column,
			err)
	}
	return err
}

// checkFunctionExists checks if a function with the given qualified name exists in the graph.
func (parser *TreeSitterParser) checkFunctionExists(tx neo4j.ManagedTransaction, qualifiedCallee string) (bool, error) {
	result, err := tx.Run(
		context.Background(),
		`MATCH (callee:Function {qualifiedName: $qualifiedName})
		 RETURN callee LIMIT 1`,
		map[string]any{
			"qualifiedName": qualifiedCallee,
		},
	)
	if err != nil {
		return false, err
	}
	return result.Next(context.Background()), nil
}

// addPendingRelation adds a CallRelation to the pendingRelations slice in a thread-safe manner.
func (parser *TreeSitterParser) addPendingRelation(rel CallRelation) {
	parser.pendingMutex.Lock()
	defer parser.pendingMutex.Unlock()
	parser.pendingRelations = append(parser.pendingRelations, rel)
}

// extractParameters extracts and returns function parameter information
func (parser *TreeSitterParser) extractParameters(node *sitter.Node) []string {
	params := []string{}
	paramList := node.ChildByFieldName("parameters")
	if paramList == nil {
		return params
	}

	for i := 0; i < int(paramList.NamedChildCount()); i++ {
		param := paramList.NamedChild(i)
		if param == nil {
			continue
		}

		// Handle different parameter node types
		switch param.Type() {
		case "parameter_declaration", "parameter":
			// Try to get parameter name and type
			nameNode := param.ChildByFieldName("name")
			typeNode := param.ChildByFieldName("type")

			if nameNode != nil {
				paramName := nameNode.Content([]byte(parser.code))
				if typeNode != nil {
					paramType := typeNode.Content([]byte(parser.code))
					params = append(params, fmt.Sprintf("%s: %s", paramName, paramType))
				} else {
					params = append(params, paramName)
				}
			}
		default:
			// Fallback to full parameter text
			params = append(params, param.Content([]byte(parser.code)))
		}
	}
	return params
}

// extractReturnType extracts and returns function return type information
func (parser *TreeSitterParser) extractReturnType(node *sitter.Node) string {
	// Try different field names for return type based on language
	returnFields := []string{"return_type", "type", "result"}

	for _, field := range returnFields {
		if returnNode := node.ChildByFieldName(field); returnNode != nil {
			// Handle different return type formats
			switch returnNode.Type() {
			case "type_identifier", "primitive_type":
				return returnNode.Content([]byte(parser.code))
			case "array_type":
				elementType := returnNode.ChildByFieldName("element")
				if elementType != nil {
					return fmt.Sprintf("[]%s", elementType.Content([]byte(parser.code)))
				}
			}
			// Fallback to full return type text
			return returnNode.Content([]byte(parser.code))
		}
	}

	return "" // No return type found
}

// extractArguments extracts and returns function call arguments
func (parser *TreeSitterParser) extractArguments(node *sitter.Node) []string {
	args := []string{}
	argList := node.ChildByFieldName("arguments")
	if argList == nil {
		return args
	}

	for i := 0; i < int(argList.NamedChildCount()); i++ {
		arg := argList.NamedChild(i)
		if arg == nil {
			continue
		}

		// Handle different argument types
		switch arg.Type() {
		case "argument_list":
			// Recursively process nested argument lists
			for j := 0; j < int(arg.NamedChildCount()); j++ {
				if nestedArg := arg.NamedChild(j); nestedArg != nil {
					args = append(args, nestedArg.Content([]byte(parser.code)))
				}
			}
		case "named_argument":
			// Handle named arguments (key: value)
			nameNode := arg.ChildByFieldName("name")
			valueNode := arg.ChildByFieldName("value")
			if nameNode != nil && valueNode != nil {
				args = append(args, fmt.Sprintf("%s: %s",
					nameNode.Content([]byte(parser.code)),
					valueNode.Content([]byte(parser.code))))
			}
		default:
			// Default to full argument text
			args = append(args, arg.Content([]byte(parser.code)))
		}
	}
	return args
}

func (parser *TreeSitterParser) handleNamespace(node *sitter.Node, tx neo4j.ManagedTransaction) error {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return fmt.Errorf("namespace name node not found")
	}

	namespaceName := nameNode.Content([]byte(parser.code))
	parser.currentNamespace = namespaceName

	_, err := tx.Run(
		context.Background(),
		`MERGE (n:Namespace {id: $id})
		ON CREATE SET 
			n.name = $name,
			n.file = $file`,
		map[string]any{
			"id":   fmt.Sprintf("%s:%s", parser.ext, namespaceName),
			"name": namespaceName,
			"file": parser.ext,
		},
	)
	return err
}

func (parser *TreeSitterParser) handleType(node *sitter.Node, tx neo4j.ManagedTransaction) error {
	nameNode := node.ChildByFieldName("name")
	if nameNode == nil {
		return fmt.Errorf("type name node not found")
	}

	typeName := nameNode.Content([]byte(parser.code))
	qualifiedName := typeName
	if parser.currentNamespace != "" {
		qualifiedName = fmt.Sprintf("%s.%s", parser.currentNamespace, typeName)
	}
	parser.currentType = qualifiedName

	_, err := tx.Run(
		context.Background(),
		`MERGE (t:Type {id: $id})
		ON CREATE SET 
			t.name = $name,
			t.qualifiedName = $qualifiedName,
			t.file = $file
		WITH t
		OPTIONAL MATCH (n:Namespace {name: $namespace})
		WHERE $namespace IS NOT NULL
		MERGE (n)-[:CONTAINS]->(t)`,
		map[string]any{
			"id":            fmt.Sprintf("%s:%s", parser.ext, qualifiedName),
			"name":          typeName,
			"qualifiedName": qualifiedName,
			"file":          parser.ext,
			"namespace":     parser.currentNamespace,
		},
	)
	return err
}

func (parser *TreeSitterParser) resolveQualifiedName(name string) string {
	// If the name already contains a namespace/type qualifier, return as is
	if strings.Contains(name, ".") {
		return name
	}

	// If we're in a type context, qualify with the current type
	if parser.currentType != "" {
		return fmt.Sprintf("%s.%s", parser.currentType, name)
	}

	// If we're in a namespace context, qualify with the current namespace
	if parser.currentNamespace != "" {
		return fmt.Sprintf("%s.%s", parser.currentNamespace, name)
	}

	return name
}

// handleGenericNode processes generic node types without specific actions.
func (parser *TreeSitterParser) handleGenericNode(node *sitter.Node, tx neo4j.ManagedTransaction) error {
	// Currently, no action is required.
	// Future enhancements can be added here if needed.
	return nil
}
