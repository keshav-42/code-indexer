FROM python:3.9-slim

WORKDIR /analyzer

# Install dependencies
COPY requirements.txt .
RUN pip install -r requirements.txt

# Copy the analyzer code
COPY . .

# Run the analyzer
CMD ["python", "analyzer.py"] 