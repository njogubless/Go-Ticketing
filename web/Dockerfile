# Two targets: `development` runs the Vite dev server with hot reload;
# `production` builds static assets and serves them from nginx.

FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json* ./
RUN npm ci --no-audit --no-fund || npm install --no-audit --no-fund

# --- development -------------------------------------------------------------
FROM deps AS development
WORKDIR /app
COPY . .
EXPOSE 5173
# --host binds beyond localhost so the port is reachable from outside the
# container; without it Vite listens on 127.0.0.1 and the published port
# appears dead.
CMD ["npx", "vite", "--host", "0.0.0.0", "--port", "5173"]

# --- build -------------------------------------------------------------------
FROM deps AS build
WORKDIR /app
COPY . .
RUN npm run build

# --- production --------------------------------------------------------------
FROM nginx:1.27-alpine AS production
COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
