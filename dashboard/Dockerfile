FROM node:18-alpine

WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1

# Install dependencies first so this layer is cached across source changes.
COPY package.json package-lock.json* ./
RUN npm install

# Build the Next.js app.
COPY . .
RUN npm run build

ENV NODE_ENV=production
EXPOSE 3000
CMD ["npm", "start"]
