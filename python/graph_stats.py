
import os
from neo4j import GraphDatabase, basic_auth

URI      = os.getenv("NEO4J_URI",      "bolt://localhost:7687")
USER     = os.getenv("NEO4J_USER",     "neo4j")
PASSWORD = os.getenv("NEO4J_PASSWORD", "password")

driver = GraphDatabase.driver(URI, auth=basic_auth(USER, PASSWORD))

def main() -> None:
    with driver.session() as sess:
        nodes = sess.run("MATCH (n) RETURN count(n) AS n").single()["n"]
        rels  = sess.run("MATCH ()-[r]->() RETURN count(r) AS r").single()["r"]
        print(f"Graph contains {nodes} nodes and {rels} relationships.")

if __name__ == "__main__":
    main()
