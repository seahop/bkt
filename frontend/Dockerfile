FROM node:26-alpine AS development

WORKDIR /app

# Copy package files
COPY package*.json ./

# Install exactly the committed lockfile (package-lock.json is in git)
RUN npm ci

# Copy source code
COPY . .

# Expose Vite dev server port (HTTPS)
EXPOSE 5173

# Start dev server (config has HTTPS enabled)
CMD ["npm", "run", "dev"]

# Production build stage
FROM node:26-alpine AS builder

WORKDIR /app

COPY package*.json ./
RUN npm ci

COPY . .
RUN npm run build

# Production stage with nginx
FROM nginx:alpine AS production

COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
