package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Node color mappings
var NodeColors = map[string]string{
	"Root":            "orange",
	"Package":         "#4287f5",
	"Function":        "#42f54e",
	"Method":          "#42f54e",
	"Struct":          "#f54242",
	"Interface":       "#f5a442",
	"ExternalService": "#f5f542",
}

// createNeo4jDriver creates a Neo4j driver with retry logic.
func createNeo4jDriver() (neo4j.DriverWithContext, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*10)
	defer cancel()

	var driver neo4j.DriverWithContext
	var err error
	retryDelay := time.Second * 5

	neo4jUri := os.Getenv("NEO4J_URI")
	neo4jUser := os.Getenv("NEO4J_USER")
	neo4jPassword := os.Getenv("NEO4J_PASSWORD")

	if neo4jUri == "" || neo4jUser == "" || neo4jPassword == "" {
		return nil, fmt.Errorf("missing required environment variables: NEO4J_URI, NEO4J_USER, NEO4J_PASSWORD")
	}

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("failed to connect to Neo4j after retries: %v", ctx.Err())
		case <-time.After(retryDelay):
			driver, err = neo4j.NewDriverWithContext(
				neo4jUri,
				neo4j.BasicAuth(neo4jUser, neo4jPassword, ""),
			)
			if err == nil {
				sess := driver.NewSession(ctx, neo4j.SessionConfig{})
				defer sess.Close(ctx)
				_, err = sess.Run(ctx, "RETURN 1", nil)
				if err == nil {
					fmt.Println("Connected to Neo4j")
					return driver, nil
				}
			}
			fmt.Printf("Retrying connection to Neo4j after %v...\n", retryDelay)
		}
	}
}

// cleanupPreviousRunData removes all nodes belonging to the given project.
func cleanupPreviousRunData(ctx context.Context, session neo4j.SessionWithContext, rootName string) error {
	_, err := session.Run(ctx, "MATCH (n) WHERE n.project = $project DETACH DELETE n", map[string]interface{}{"project": rootName})
	if err != nil {
		return fmt.Errorf("failed to clean up previous data: %v", err)
	}
	fmt.Println("Cleaned up data from previous run")
	return nil
}

// createUniquenessConstraints ensures unique constraints on various node types.
func createUniquenessConstraints(ctx context.Context, session neo4j.SessionWithContext) error {
	constraints := []string{
		"CREATE CONSTRAINT IF NOT EXISTS FOR (r:Root) REQUIRE r.id IS UNIQUE",
		"CREATE CONSTRAINT IF NOT EXISTS FOR (p:Package) REQUIRE p.id IS UNIQUE",
		"CREATE CONSTRAINT IF NOT EXISTS FOR (s:Struct) REQUIRE s.id IS UNIQUE",
		"CREATE CONSTRAINT IF NOT EXISTS FOR (f:Function) REQUIRE f.id IS UNIQUE",
		"CREATE CONSTRAINT IF NOT EXISTS FOR (m:Method) REQUIRE m.id IS UNIQUE",
		"CREATE CONSTRAINT IF NOT EXISTS FOR (i:Interface) REQUIRE i.id IS UNIQUE",
	}
	for _, constraint := range constraints {
		_, err := session.Run(ctx, constraint, nil)
		if err != nil {
			return fmt.Errorf("failed to create uniqueness constraint: %v", err)
		}
	}
	return nil
}

// findGoFiles returns a list of Go source files in the project (excluding _test.go).
func findGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// createUrl constructs a source code URL linking to a specific line.
func createUrl(baseUrl, filePath, projectRoot string, lineNumber int) string {
	relativePath := strings.TrimPrefix(filePath, projectRoot)
	// Ensure baseUrl ends with '/' if not present
	if !strings.HasSuffix(baseUrl, "/") {
		baseUrl += "/"
	}
	return fmt.Sprintf("%s%s#%d", baseUrl, strings.TrimPrefix(relativePath, "/"), lineNumber)
}

func main() {
	rootName := os.Getenv("ROOT_NAME")
	if rootName == "" {
		log.Fatal("ROOT_NAME environment variable is not set")
	}
	baseUrl := os.Getenv("BASE_URL")
	if baseUrl == "" {
		baseUrl = "http://localhost"
	}
	projectRoot := "/app"

	ctx := context.Background()
	driver, err := createNeo4jDriver()
	if err != nil {
		log.Fatalf("Failed to connect to Neo4j: %v", err)
	}
	defer driver.Close(ctx)

	// Create a session to perform initial setup (not concurrently)
	setupSession := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer setupSession.Close(ctx)

	// Clean up previous data and create constraints
	err = cleanupPreviousRunData(ctx, setupSession, rootName)
	if err != nil {
		log.Fatalf("Cleanup error: %v", err)
	}

	err = createUniquenessConstraints(ctx, setupSession)
	if err != nil {
		log.Fatalf("Constraint creation error: %v", err)
	}

	// Create root node with unique ID
	rootId := "root:" + rootName
	_, err = setupSession.Run(ctx,
		"MERGE (r:Root {id: $id}) ON CREATE SET r.name = $name, r.project = $project, r.color = $color",
		map[string]interface{}{
			"id":      rootId,
			"name":    rootName,
			"project": rootName,
			"color":   NodeColors["Root"],
		})
	if err != nil {
		log.Fatalf("Failed to create root node: %v", err)
	}

	// Find Go files
	goFiles, err := findGoFiles(projectRoot)
	if err != nil {
		log.Fatalf("Failed to find Go files: %v", err)
	}

	fset := token.NewFileSet()
	var wg sync.WaitGroup

	// Process each Go file concurrently. Each goroutine gets its own session.
	for _, filePath := range goFiles {
		wg.Add(1)
		go func(filePath string) {
			defer wg.Done()
			sess := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
			defer sess.Close(ctx)
			processGoFile(ctx, filePath, fset, projectRoot, baseUrl, sess, rootName, rootId)
		}(filePath)
	}
	wg.Wait()
}

// processGoFile parses a Go file, creates/upserts a Package node,
// and processes types (structs/interfaces) and functions/methods.
func processGoFile(ctx context.Context, filePath string, fset *token.FileSet, projectRoot, baseUrl string, session neo4j.SessionWithContext, rootName, rootId string) {
	node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
	if err != nil {
		log.Printf("Failed to parse Go file %s: %v", filePath, err)
		return
	}

	packageName := node.Name.Name
	packageId := fmt.Sprintf("%s:%s", rootName, packageName)

	// Create or merge Package node
	_, err = session.Run(ctx,
		"MERGE (p:Package {id: $id}) "+
			"ON CREATE SET p.name = $name, p.project = $project, p.color = $color, p.url = $url",
		map[string]interface{}{
			"id":      packageId,
			"name":    packageName,
			"project": rootName,
			"color":   NodeColors["Package"],
			"url":     createUrl(baseUrl, filePath, projectRoot, 1),
		})
	if err != nil {
		log.Printf("Failed to create package node: %v", err)
		return
	}

	// Link Root to this package (if not already linked)
	_, err = session.Run(ctx,
		"MATCH (r:Root {id:$rootId}), (p:Package {id:$packageId}) "+
			"MERGE (r)-[:CONTAINS]->(p)",
		map[string]interface{}{
			"rootId":    rootId,
			"packageId": packageId,
		})
	if err != nil {
		log.Printf("Failed to link Root and Package: %v", err)
	}

	// Build an import map for external references
	importMap := buildImportMap(node)

	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.GenDecl:
			processGenDecl(ctx, x, packageId, filePath, fset, rootName, session, baseUrl, projectRoot)
		case *ast.FuncDecl:
			processFuncDecl(ctx, x, packageId, filePath, fset, rootName, session, baseUrl, projectRoot, importMap)
		}
		return true
	})
}

// buildImportMap creates a mapping of import alias -> package path.
func buildImportMap(file *ast.File) map[string]string {
	importMap := make(map[string]string)
	for _, im := range file.Imports {
		path := strings.Trim(im.Path.Value, `"`)
		var alias string
		if im.Name != nil && im.Name.Name != "_" && im.Name.Name != "." {
			alias = im.Name.Name
		} else {
			// Default alias is the last component of the path
			parts := strings.Split(path, "/")
			alias = parts[len(parts)-1]
		}
		importMap[alias] = path
	}
	return importMap
}

// processGenDecl handles type declarations (structs, interfaces).
func processGenDecl(ctx context.Context, x *ast.GenDecl, packageId, filePath string, fset *token.FileSet, rootName string, session neo4j.SessionWithContext, baseUrl, projectRoot string) {
	for _, spec := range x.Specs {
		if typeSpec, ok := spec.(*ast.TypeSpec); ok {
			typeName := typeSpec.Name.Name
			typeId := fmt.Sprintf("%s.%s", packageId, typeName)
			line := fset.Position(x.Pos()).Line

			switch {
			case typeSpec.Type.(*ast.StructType) != nil:
				_, err := session.Run(ctx,
					"MERGE (s:Struct {id: $id}) "+
						"ON CREATE SET s.name = $name, s.packageId = $packageId, s.project = $project, s.color = $color, s.url = $url",
					map[string]interface{}{
						"id":        typeId,
						"name":      typeName,
						"packageId": packageId,
						"project":   rootName,
						"color":     NodeColors["Struct"],
						"url":       createUrl(baseUrl, filePath, projectRoot, line),
					})
				if err != nil {
					log.Printf("Failed to create struct node: %v", err)
					continue
				}
				// Link Package to Struct
				_, err = session.Run(ctx,
					"MATCH (p:Package {id:$packageId}), (s:Struct {id:$typeId}) "+
						"MERGE (p)-[:DECLARES]->(s)",
					map[string]interface{}{
						"packageId": packageId,
						"typeId":    typeId,
					})
				if err != nil {
					log.Printf("Failed to link Package and Struct: %v", err)
				}

			case typeSpec.Type.(*ast.InterfaceType) != nil:
				_, err := session.Run(ctx,
					"MERGE (i:Interface {id: $id}) "+
						"ON CREATE SET i.name = $name, i.packageId = $packageId, i.project = $project, i.color = $color, i.url = $url",
					map[string]interface{}{
						"id":        typeId,
						"name":      typeName,
						"packageId": packageId,
						"project":   rootName,
						"color":     NodeColors["Interface"],
						"url":       createUrl(baseUrl, filePath, projectRoot, line),
					})
				if err != nil {
					log.Printf("Failed to create interface node: %v", err)
					continue
				}
				// Link Package to Interface
				_, err = session.Run(ctx,
					"MATCH (p:Package {id:$packageId}), (i:Interface {id:$typeId}) "+
						"MERGE (p)-[:DECLARES]->(i)",
					map[string]interface{}{
						"packageId": packageId,
						"typeId":    typeId,
					})
				if err != nil {
					log.Printf("Failed to link Package and Interface: %v", err)
				}
			}
		}
	}
}

// processFuncDecl handles function and method declarations, and also tracks calls within them.
func processFuncDecl(ctx context.Context, x *ast.FuncDecl, packageId, filePath string, fset *token.FileSet, rootName string, session neo4j.SessionWithContext, baseUrl, projectRoot string, importMap map[string]string) {
	funcName := x.Name.Name
	line := fset.Position(x.Pos()).Line

	// For uniqueness, we can just use the function name for now. For more uniqueness, consider parameters.
	funcSignature := funcName
	var funcId string

	if x.Recv != nil && len(x.Recv.List) > 0 {
		recvType := extractReceiverType(x.Recv)
		if recvType != "" {
			structId := fmt.Sprintf("%s.%s", packageId, recvType)
			funcId = fmt.Sprintf("%s.%s", structId, funcSignature)

			// Create or merge Method node
			_, err := session.Run(ctx,
				"MERGE (m:Method {id: $id}) "+
					"ON CREATE SET m.name = $name, m.structId = $structId, m.project = $project, m.color = $color, m.url = $url",
				map[string]interface{}{
					"id":       funcId,
					"name":     funcName,
					"structId": structId,
					"project":  rootName,
					"color":    NodeColors["Method"],
					"url":      createUrl(baseUrl, filePath, projectRoot, line),
				})
			if err != nil {
				log.Printf("Failed to create method node: %v", err)
			}
			// Link Struct to Method
			_, err = session.Run(ctx,
				"MATCH (s:Struct {id:$structId}), (m:Method {id:$funcId}) "+
					"MERGE (s)-[:DECLARES]->(m)",
				map[string]interface{}{
					"structId": structId,
					"funcId":   funcId,
				})
			if err != nil {
				log.Printf("Failed to link Struct and Method: %v", err)
			}
		}
	} else {
		funcId = fmt.Sprintf("%s.%s", packageId, funcSignature)

		// Create or merge Function node
		_, err := session.Run(ctx,
			"MERGE (f:Function {id: $id}) "+
				"ON CREATE SET f.name = $name, f.packageId = $packageId, f.project = $project, f.color = $color, f.url = $url",
			map[string]interface{}{
				"id":        funcId,
				"name":      funcName,
				"packageId": packageId,
				"project":   rootName,
				"color":     NodeColors["Function"],
				"url":       createUrl(baseUrl, filePath, projectRoot, line),
			})
		if err != nil {
			log.Printf("Failed to create function node: %v", err)
		}
		// Link Package to Function
		_, err = session.Run(ctx,
			"MATCH (p:Package {id:$packageId}), (f:Function {id:$funcId}) "+
				"MERGE (p)-[:DECLARES]->(f)",
			map[string]interface{}{
				"packageId": packageId,
				"funcId":    funcId,
			})
		if err != nil {
			log.Printf("Failed to link Package and Function: %v", err)
		}
	}

	// If the function body is nil (like in external decl), skip
	if x.Body == nil {
		return
	}

	// Traverse the function body to find called methods/functions
	ast.Inspect(x.Body, func(n ast.Node) bool {
		callExpr, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		var calleeId, calleeName, calleePackageId string
		var calleeLabel = "Function"
		switch fun := callExpr.Fun.(type) {
		case *ast.Ident:
			// Unqualified call, same package
			calleeName = fun.Name
			calleePackageId = packageId
			calleeId = fmt.Sprintf("%s.%s", calleePackageId, calleeName)

		case *ast.SelectorExpr:
			// Could be pkg.Func or var.Method
			if pkgIdent, ok := fun.X.(*ast.Ident); ok {
				alias := pkgIdent.Name
				// Check if alias is in importMap => external package call
				if pkgPath, found := importMap[alias]; found {
					// External package function
					calleePackageId = fmt.Sprintf("%s:%s", rootName, pkgPath)
					calleeName = fun.Sel.Name
					calleeId = fmt.Sprintf("%s.%s", calleePackageId, calleeName)
					// Ensure external package node exists
					ensurePackageNode(ctx, session, calleePackageId, pkgPath, rootName, "")
				} else {
					// It's not a known import alias. This might be a method call on a variable of type struct.
					// Without type analysis, we'll just treat it as a function call in the same package for now.
					calleeName = fun.Sel.Name
					calleePackageId = packageId
					calleeId = fmt.Sprintf("%s.%s", calleePackageId, calleeName)
				}
			}

		default:
			// Other call expression forms can be ignored or treated as unknown.
			return true
		}

		// Create or merge the callee node as a function by default
		_, err := session.Run(ctx,
			"MERGE (callee:Function {id: $calleeId}) "+
				"ON CREATE SET callee.name=$calleeName, callee.packageId=$calleePackageId, callee.project=$project, callee.color=$color",
			map[string]interface{}{
				"calleeId":        calleeId,
				"calleeName":      calleeName,
				"calleePackageId": calleePackageId,
				"project":         rootName,
				"color":           NodeColors[calleeLabel],
			})
		if err != nil {
			log.Printf("Failed to create callee node: %v", err)
		}

		// Create CALLS relationship
		_, err = session.Run(ctx,
			"MATCH (caller {id:$callerId}), (callee {id:$calleeId}) MERGE (caller)-[:CALLS]->(callee)",
			map[string]interface{}{
				"callerId": funcId,
				"calleeId": calleeId,
			})
		if err != nil {
			log.Printf("Failed to create CALLS relationship: %v", err)
		}

		return true
	})

}

// ensurePackageNode ensures that a package node exists for external calls.
func ensurePackageNode(ctx context.Context, session neo4j.SessionWithContext, packageId, packageName, rootName, baseUrl string) {
	if baseUrl == "" {
		baseUrl = "http://localhost"
	}
	_, err := session.Run(ctx,
		"MERGE (p:Package {id:$id}) "+
			"ON CREATE SET p.name=$name, p.project=$project, p.color=$color",
		map[string]interface{}{
			"id":      packageId,
			"name":    packageName,
			"project": rootName,
			"color":   NodeColors["Package"],
		})
	if err != nil {
		log.Printf("Failed to ensure package node for %s: %v", packageId, err)
	}
	// Optionally link the external package to root as well:
	_, err = session.Run(ctx,
		"MATCH (r:Root {project:$project}), (p:Package {id:$packageId}) MERGE (r)-[:CONTAINS]->(p)",
		map[string]interface{}{
			"project":   rootName,
			"packageId": packageId,
		})
	if err != nil {
		log.Printf("Failed to link root to external package %s: %v", packageId, err)
	}
}

// extractReceiverType extracts the receiver type name from a method declaration.
func extractReceiverType(recv *ast.FieldList) string {
	if len(recv.List) == 0 {
		return ""
	}
	switch expr := recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if ident, ok := expr.X.(*ast.Ident); ok {
			return ident.Name
		}
	case *ast.Ident:
		return expr.Name
	}
	return ""
}
