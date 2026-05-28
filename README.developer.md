# How to Run
1. Run docker compose launcher by running:
    ```
    docker compose --profile launcher up
    ```

2. Run picoclaw gateway:
    ```
    docker compose --profile gateway up
    ```

3. Run picoclaw agent (one-shot):
    ```
    docker compose --profile agent run --rm picoclaw-agent -m "Hello"
    ```

4. Run picoclaw agent (interactive):
    ```
    docker compose --profile agent run --rm picoclaw-agent
    ```
