.PHONY: build build-server build-web build-acp dev install clean

# Build everything
build: build-server build-web build-acp

build-server:
	CGO_CFLAGS="-DSQLITE_ENABLE_FTS5" go build -o pokegents-dashboard .

build-web:
	../scripts/fetch-pokesprite-assets.sh
	cd web && npm ci --silent && npm run build

build-acp:
	cd acp-fork && npm ci --silent && if [ -f tsconfig.json ]; then npm run build; else test -f dist/index.js; fi

# Dev mode: run both servers
dev:
	@echo "Terminal 1:  go run . serve"
	@echo "Terminal 2:  cd web && npm run dev"

# Install: build + register hooks (use top-level install.sh instead)
install: build
	@chmod +x hooks/dashboard-notify.sh
	@echo ""
	@echo "✓ Dashboard built"
	@echo "  Run ../install.sh to register hooks"
	@echo "  Start:  ./pokegents-dashboard serve"

clean:
	rm -f pokegents-dashboard
	rm -rf web/dist web/node_modules acp-fork/dist acp-fork/node_modules
