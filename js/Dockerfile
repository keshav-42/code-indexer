# Use the official Node.js image
FROM node:18

# Set working directory
WORKDIR /analyzer

# Copy package.json and install dependencies
COPY package.json package-lock.json ./
RUN npm install

# Copy the rest of the application code
COPY . .

# Command to run the analyzer
CMD ["node", "analyzer.js"]
