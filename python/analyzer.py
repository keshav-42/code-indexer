import ast
import os
import time
from neo4j import GraphDatabase

# Node color mappings
NODE_COLORS = {
    'Root': 'orange',
    'Class': '#4287f5',
    'Function': '#42f54e',
    'Method': '#42f54e',
    'ExternalService': '#f5f542',
    'MongoCollection': '#f5a442',
}

def create_neo4j_driver():
    uri = os.getenv("NEO4J_URI")
    username = os.getenv("NEO4J_USER")
    password = os.getenv("NEO4J_PASSWORD")

    if not uri or not username or not password:
        raise RuntimeError("NEO4J_URI, NEO4J_USER, and NEO4J_PASSWORD environment variables are required.")

    driver = GraphDatabase.driver(uri, auth=(username, password))
    max_retries = 100
    delay = 5
    for attempt in range(max_retries):
        try:
            with driver.session() as session:
                session.run("RETURN 1")
            print("Connected to Neo4j")
            return driver
        except Exception as e:
            print(f"Attempt {attempt + 1}: Neo4j connection failed ({e}), retrying in {delay} seconds...")
            time.sleep(delay)
            delay *= 2
    raise Exception("Failed to connect to Neo4j after several attempts")

def cleanup_previous_run_data(session, root_name):
    session.run("MATCH (n) WHERE n.project = $project DETACH DELETE n", {"project": root_name})
    print("Cleaned up data from previous run")

def create_uniqueness_constraints(session):
    # Add a constraint for Root as well
    session.run("CREATE CONSTRAINT IF NOT EXISTS FOR (r:Root) REQUIRE r.id IS UNIQUE")
    session.run("CREATE CONSTRAINT IF NOT EXISTS FOR (c:Class) REQUIRE c.id IS UNIQUE")
    session.run("CREATE CONSTRAINT IF NOT EXISTS FOR (f:Function) REQUIRE f.id IS UNIQUE")
    session.run("CREATE CONSTRAINT IF NOT EXISTS FOR (m:Method) REQUIRE m.id IS UNIQUE")

def analyze_file(file_path, session, root_name, base_url):
    with open(file_path, 'r', encoding='utf-8') as file:
        code = file.read()
    project_root = '/app'

    def create_url(file_path, line_number):
        relative_path = os.path.relpath(file_path, project_root)
        return f"{base_url}/{relative_path}#{line_number}"

    try:
        tree = ast.parse(code, filename=file_path)
        AnalyzerVisitor(session, root_name, file_path, create_url).visit(tree)
    except Exception as e:
        print(f"Error parsing {file_path}: {e}")

class AnalyzerVisitor(ast.NodeVisitor):
    def __init__(self, session, root_name, file_path, create_url):
        self.session = session
        self.root_name = root_name
        self.file_path = file_path
        self.create_url = create_url
        self.current_class = None
        self.root_id = f"root:{self.root_name}"

    def merge_node(self, label, node_id, properties):
        # Merge a node with ON CREATE and ON MATCH to keep properties updated
        set_clauses = ", ".join([f"n.{key} = ${key}" for key in properties.keys()])
        query = (f"MERGE (n:{label} {{id: $id}}) "
                 f"ON CREATE SET {set_clauses} "
                 f"ON MATCH SET {set_clauses}")
        params = {"id": node_id}
        params.update(properties)
        self.session.run(query, params)

    def create_relationship(self, start_id, end_id, rel_type):
        self.session.run(
            f"MATCH (a {{id:$startId}}), (b {{id:$endId}}) MERGE (a)-[:{rel_type}]->(b)",
            {"startId": start_id, "endId": end_id}
        )

    def visit_ClassDef(self, node):
        class_name = node.name
        class_id = f"{self.root_name}:{class_name}"
        url = self.create_url(self.file_path, node.lineno)

        # Create or merge Class node with color
        self.merge_node("Class", class_id, {
            "name": class_name,
            "project": self.root_name,
            "file": self.file_path,
            "url": url,
            "color": NODE_COLORS["Class"]
        })

        # Link Root to Class
        self.create_relationship(self.root_id, class_id, "DECLARES")

        # Process methods
        self.current_class = class_id
        self.generic_visit(node)
        self.current_class = None

    def visit_FunctionDef(self, node):
        function_name = node.name
        url = self.create_url(self.file_path, node.lineno)
        if self.current_class:
            # It's a method
            function_id = f"{self.current_class}.{function_name}"
            self.merge_node("Method", function_id, {
                "name": function_name,
                "classId": self.current_class,
                "project": self.root_name,
                "file": self.file_path,
                "url": url,
                "color": NODE_COLORS["Method"]
            })
            # Link Class to Method
            self.create_relationship(self.current_class, function_id, "DECLARES")
        else:
            # It's a function
            function_id = f"{self.root_name}:{function_name}"
            self.merge_node("Function", function_id, {
                "name": function_name,
                "project": self.root_name,
                "file": self.file_path,
                "url": url,
                "color": NODE_COLORS["Function"]
            })
            # Link Root to Function
            self.create_relationship(self.root_id, function_id, "DECLARES")

        self.generic_visit(node)

def analyze_directory(dir_path, session, root_name, base_url):
    for root, dirs, files in os.walk(dir_path):
        for file in files:
            if file.endswith('.py') and not file.startswith('.'):
                file_path = os.path.join(root, file)
                analyze_file(file_path, session, root_name, base_url)

def main():
    root_name = os.getenv('ROOT_NAME') or 'UnknownRoot'
    base_url = os.getenv('BASE_URL') or 'http://localhost'
    project_root = '/app'  # Not directly used here, but can be utilized in URL creation

    driver = create_neo4j_driver()
    with driver.session() as session:
        # Clean up previous data and create constraints
        cleanup_previous_run_data(session, root_name)
        create_uniqueness_constraints(session)

        # Create root node with a unique id
        root_id = f"root:{root_name}"
        session.run(
            "MERGE (r:Root {id:$id}) "
            "ON CREATE SET r.name=$name, r.project=$project, r.color=$color "
            "ON MATCH SET r.name=$name, r.project=$project, r.color=$color",
            {"id": root_id, "name": root_name, "project": root_name, "color": NODE_COLORS["Root"]}
        )

        # Analyze the project directory
        analyze_directory('/app', session, root_name, base_url)

    driver.close()

if __name__ == "__main__":
    main()
