const fs = require("fs");
const path = require("path");
const babelParser = require("@babel/parser");
const traverse = require("@babel/traverse").default;
const neo4j = require("neo4j-driver");

const NodeColors = {
	Root: "orange",
	Class: "#4287f5",
	Function: "#42f54e",
	Method: "#42f54e",
	ReactComponent: "#f54242",
	ExternalService: "#f5f542",
	MongoCollection: "#f5a442",
};

async function createNeo4jDriver(uri, username, password) {
	const maxRetries = 100;
	let delay = 5000;

	for (let attempt = 0; attempt < maxRetries; attempt++) {
		try {
			const driver = neo4j.driver(uri, neo4j.auth.basic(username, password));
			const session = driver.session();
			await session.run("RETURN 1");
			await session.close();
			console.log("Connected to Neo4j");
			return driver;
		} catch (error) {
			console.log(
				`Attempt ${attempt + 1}: Neo4j connection failed, retrying in ${
					delay / 1000
				} seconds...`
			);
			await new Promise((resolve) => setTimeout(resolve, delay));
			delay *= 2;
		}
	}
	throw new Error("Failed to connect to Neo4j after several attempts");
}

async function createUniquenessConstraints(session) {
	await session.run(
		"CREATE CONSTRAINT IF NOT EXISTS FOR (r:Root) REQUIRE r.id IS UNIQUE"
	);
	await session.run(
		"CREATE CONSTRAINT IF NOT EXISTS FOR (c:Class) REQUIRE c.id IS UNIQUE"
	);
	await session.run(
		"CREATE CONSTRAINT IF NOT EXISTS FOR (f:Function) REQUIRE f.id IS UNIQUE"
	);
	await session.run(
		"CREATE CONSTRAINT IF NOT EXISTS FOR (m:Method) REQUIRE m.id IS UNIQUE"
	);
	await session.run(
		"CREATE CONSTRAINT IF NOT EXISTS FOR (rc:ReactComponent) REQUIRE rc.id IS UNIQUE"
	);
}

async function cleanupPreviousRunData(session, rootName) {
	await session.run("MATCH (n) WHERE n.project = $project DETACH DELETE n", {
		project: rootName,
	});
	console.log("Cleaned up data from previous run");
}

function createUrl(baseUrl, filePath, projectRoot, lineNumber) {
	const relativePath = path.relative(projectRoot, filePath);
	// Ensure baseUrl ends with '/'
	if (!baseUrl.endsWith("/")) {
		baseUrl += "/";
	}
	return `${baseUrl}${relativePath}#${lineNumber}`;
}

async function analyzeFile(
	filePath,
	session,
	rootName,
	baseUrl,
	projectRoot,
	rootId
) {
	const code = fs.readFileSync(filePath, "utf-8");
	let ast;
	try {
		ast = babelParser.parse(code, {
			sourceType: "module",
			plugins: [
				"jsx",
				"typescript",
				"classProperties",
				"decorators-legacy",
				"exportDefaultFrom",
				"exportNamespaceFrom",
				"dynamicImport",
				"objectRestSpread",
				"optionalChaining",
				"nullishCoalescingOperator",
			],
		});
	} catch (err) {
		console.error(`Failed to parse ${filePath}:`, err);
		return;
	}

	// We'll store queries and run them after traversal to ensure consistency.
	const queries = [];
	const processedNodes = new Set();

	function mergeNodeQuery(
		labels,
		id,
		properties,
		onCreateExtra = "",
		onMatchExtra = ""
	) {
		const labelString = Array.isArray(labels) ? labels.join(":") : labels;
		let setClause = Object.keys(properties)
			.map((key) => `n.${key} = $${key}`)
			.join(", ");
		let query = `MERGE (n:${labelString} {id: $id}) ON CREATE SET ${setClause}`;
		if (onCreateExtra) query += `, ${onCreateExtra}`;
		if (onMatchExtra) query += ` ON MATCH SET ${setClause}`;
		return { query, params: { id, ...properties } };
	}

	function createRelationshipQuery(sourceId, targetId, relType) {
		return {
			query: `MATCH (a {id: $sourceId}), (b {id: $targetId}) MERGE (a)-[:${relType}]->(b)`,
			params: { sourceId, targetId },
		};
	}

	// Helpers to create different node types and link them to Root
	function ensureClassNode(classId, className, filePath, line) {
		if (!processedNodes.has(classId)) {
			processedNodes.add(classId);
			queries.push(
				mergeNodeQuery("Class", classId, {
					name: className,
					project: rootName,
					file: filePath,
					color: NodeColors.Class,
					url: createUrl(baseUrl, filePath, projectRoot, line),
				})
			);
			// Link Root to Class
			queries.push(createRelationshipQuery(rootId, classId, "DECLARES"));
		}
	}

	function ensureFunctionNode(funcId, funcName, filePath, line) {
		if (!processedNodes.has(funcId)) {
			processedNodes.add(funcId);
			queries.push(
				mergeNodeQuery("Function", funcId, {
					name: funcName,
					project: rootName,
					file: filePath,
					color: NodeColors.Function,
					url: createUrl(baseUrl, filePath, projectRoot, line),
				})
			);
			// Link Root to Function
			queries.push(createRelationshipQuery(rootId, funcId, "DECLARES"));
		}
	}

	function ensureMethodNode(methodId, methodName, filePath, line) {
		if (!processedNodes.has(methodId)) {
			processedNodes.add(methodId);
			queries.push(
				mergeNodeQuery("Method", methodId, {
					name: methodName,
					project: rootName,
					file: filePath,
					color: NodeColors.Method,
					url: createUrl(baseUrl, filePath, projectRoot, line),
				})
			);
			// Also link Root to Method to keep everything accessible
			queries.push(createRelationshipQuery(rootId, methodId, "DECLARES"));
		}
	}

	function ensureReactComponentNode(
		componentId,
		componentName,
		filePath,
		line
	) {
		if (!processedNodes.has(componentId)) {
			processedNodes.add(componentId);
			queries.push(
				mergeNodeQuery("ReactComponent", componentId, {
					name: componentName,
					project: rootName,
					file: filePath,
					color: NodeColors.ReactComponent,
					url: createUrl(baseUrl, filePath, projectRoot, line),
				})
			);
			// Link Root to ReactComponent
			queries.push(createRelationshipQuery(rootId, componentId, "DECLARES"));
		}
	}

	function ensureCalleeNode(calleeId, calleeName) {
		// Default to Function if unknown
		if (!processedNodes.has(calleeId)) {
			processedNodes.add(calleeId);
			queries.push(
				mergeNodeQuery("Function", calleeId, {
					name: calleeName,
					project: rootName,
				})
			);
			// Link Root to it as well
			queries.push(createRelationshipQuery(rootId, calleeId, "DECLARES"));
		}
	}

	function createCallsRelationship(callerId, calleeId) {
		queries.push(createRelationshipQuery(callerId, calleeId, "CALLS"));
	}

	traverse(ast, {
		enter(path) {
			if (path.node.loc) {
				path.node.loc.file = filePath;
			}
		},
		ClassDeclaration(path) {
			const { node } = path;
			if (!node.id) return;
			const className = node.id.name;
			const classId = `${rootName}:${className}`;
			const line = node.loc.start.line;
			ensureClassNode(classId, className, filePath, line);

			// Also handle class methods inside the class body
			node.body.body.forEach((classMember) => {
				if (
					classMember.type === "ClassMethod" &&
					classMember.key.type === "Identifier"
				) {
					const methodName = classMember.key.name;
					const methodId = `${classId}.${methodName}`;
					const methodLine = classMember.loc.start.line;
					ensureMethodNode(methodId, methodName, filePath, methodLine);

					// Link Class to Method
					queries.push(createRelationshipQuery(classId, methodId, "DECLARES"));
				}
			});
		},
		FunctionDeclaration(path) {
			const { node } = path;
			if (!node.id) return;
			const functionName = node.id.name;
			const functionId = `${rootName}:${functionName}`;
			const line = node.loc.start.line;
			ensureFunctionNode(functionId, functionName, filePath, line);
		},
		ClassMethod(path) {
			// For class methods handled inside ClassDeclaration, but in case we want standalone handling:
			const { node } = path;
			if (
				!path.parentPath.parent ||
				path.parentPath.parent.type !== "ClassDeclaration"
			)
				return;
			const className = path.parentPath.parent.id.name;
			const classId = `${rootName}:${className}`;
			if (node.key.type === "Identifier") {
				const methodName = node.key.name;
				const methodId = `${classId}.${methodName}`;
				const line = node.loc.start.line;
				ensureMethodNode(methodId, methodName, filePath, line);
				queries.push(createRelationshipQuery(classId, methodId, "DECLARES"));
			}
		},
		CallExpression(path) {
			const { node } = path;
			let calleeName = "";
			let calleeId = "";
			const callee = node.callee;
			if (callee.type === "Identifier") {
				calleeName = callee.name;
				calleeId = `${rootName}:${calleeName}`;
			} else if (callee.type === "MemberExpression") {
				const objectName =
					callee.object && callee.object.type === "Identifier"
						? callee.object.name
						: "unknownObj";
				const propertyName =
					callee.property && callee.property.type === "Identifier"
						? callee.property.name
						: "unknownProp";
				calleeName = `${objectName}.${propertyName}`;
				calleeId = `${rootName}:${calleeName}`;
			} else {
				// Unsupported callee type, ignore
				return;
			}

			// Determine callerId
			const callerFunc = path.getFunctionParent();
			let callerId = "";
			if (callerFunc && callerFunc.node) {
				if (
					callerFunc.node.type === "FunctionDeclaration" &&
					callerFunc.node.id
				) {
					callerId = `${rootName}:${callerFunc.node.id.name}`;
				} else if (callerFunc.node.type === "ClassMethod") {
					const classNode = callerFunc.parentPath.parent;
					if (
						classNode &&
						classNode.type === "ClassDeclaration" &&
						classNode.id
					) {
						const className = classNode.id.name;
						const methodName = callerFunc.node.key.name;
						callerId = `${rootName}:${className}.${methodName}`;
					}
				}
			}
			// If we cannot determine callerId, skip relationship
			if (!callerId) {
				// Still ensure callee node exists if needed
				ensureCalleeNode(calleeId, calleeName);
				return;
			}

			// Ensure both caller and callee exist
			ensureCalleeNode(calleeId, calleeName);
			createCallsRelationship(callerId, calleeId);
		},
		JSXElement(path) {
			const { node } = path;
			const openingName = node.openingElement.name;
			if (openingName && openingName.type === "JSXIdentifier") {
				const componentName = openingName.name;
				const componentId = `${rootName}:${componentName}`;
				const line = node.loc.start.line;
				ensureReactComponentNode(componentId, componentName, filePath, line);
			}
		},
	});

	// After traversal, run all queries in a single transaction
	if (queries.length > 0) {
		const tx = session.beginTransaction();
		try {
			for (const q of queries) {
				await tx.run(q.query, q.params);
			}
			await tx.commit();
		} catch (err) {
			console.error(`Failed to execute queries for file ${filePath}:`, err);
			await tx.rollback();
		}
	}
}

async function analyzeDirectory(
	dir,
	session,
	rootName,
	baseUrl,
	projectRoot,
	rootId
) {
	const files = fs.readdirSync(dir);
	for (const file of files) {
		const filePath = path.join(dir, file);
		const stat = fs.lstatSync(filePath);
		if (stat.isDirectory()) {
			if (file === "node_modules" || file.startsWith(".")) {
				continue;
			}
			await analyzeDirectory(
				filePath,
				session,
				rootName,
				baseUrl,
				projectRoot,
				rootId
			);
		} else if (
			file.endsWith(".js") ||
			file.endsWith(".jsx") ||
			file.endsWith(".ts") ||
			file.endsWith(".tsx")
		) {
			await analyzeFile(
				filePath,
				session,
				rootName,
				baseUrl,
				projectRoot,
				rootId
			);
		}
	}
}

async function main() {
	const rootName = process.env.ROOT_NAME || "UnknownRoot";
	const baseUrl = process.env.BASE_URL || "http://localhost";
	const projectRoot = "/app";

	const neo4jUri = process.env.NEO4J_URI;
	const neo4jUser = process.env.NEO4J_USER;
	const neo4jPassword = process.env.NEO4J_PASSWORD;

	if (!neo4jUri || !neo4jUser || !neo4jPassword) {
		console.error(
			"Error: Missing required environment variables: NEO4J_URI, NEO4J_USER, NEO4J_PASSWORD"
		);
		process.exit(1);
	}

	const driver = await createNeo4jDriver(neo4jUri, neo4jUser, neo4jPassword);

	const session = driver.session();

	try {
		await cleanupPreviousRunData(session, rootName);
		await createUniquenessConstraints(session);

		const rootId = `root:${rootName}`;
		await session.run(
			"MERGE (r:Root {id:$id}) ON CREATE SET r.name=$name, r.project=$project, r.color=$color",
			{ id: rootId, name: rootName, project: rootName, color: NodeColors.Root }
		);

		await analyzeDirectory(
			projectRoot,
			session,
			rootName,
			baseUrl,
			projectRoot,
			rootId
		);
	} catch (error) {
		console.error("Error:", error);
	} finally {
		await session.close();
		await driver.close();
	}
}

main().catch((error) => {
	console.error("Main Error:", error);
});
