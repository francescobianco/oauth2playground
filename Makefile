
push:
	@git add .
	@git commit -m "Update at $$(date)" || true
	@git push