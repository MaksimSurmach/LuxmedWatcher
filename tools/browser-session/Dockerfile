FROM mcr.microsoft.com/playwright:v1.65.1-noble
RUN apt-get update && apt-get install -y --no-install-recommends x11vnc xauth xvfb && rm -rf /var/lib/apt/lists/*
WORKDIR /worker
COPY package*.json ./
RUN npm ci --omit=dev && install -d -m 700 -o pwuser -g pwuser /profile
COPY server.mjs session.mjs portal-api.mjs viewer.html viewer.mjs viewer-copy.mjs ./
USER pwuser
CMD ["xvfb-run", "-a", "-s", "-screen 0 1200x800x24 -nolisten tcp", "sh", "-c", "x11vnc -localhost -forever -nopw -nosel -quiet >/dev/null 2>&1 & exec node server.mjs"]
