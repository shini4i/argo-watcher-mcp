import uvicorn

if __name__ == "__main__":
    # For a real application, use a proper argument parser.
    # This is simplified for clarity.
    uvicorn.run(
        app="argo_watcher_mcp.app:create_app",
        factory=True,
        host="0.0.0.0",
        port=8000,
    )
