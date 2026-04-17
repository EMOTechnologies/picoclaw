# Chromium Memory Optimization for Picoclaw Heavy

> When using picoclaw-heavy with agent-browser (Chromium), memory usage increases significantly. This guide helps optimize memory usage for production deployments.

## Memory Usage Breakdown

### Base Memory Usage
- **Picoclaw binary**: 10-20MB
- **Node.js 24 runtime**: 50-100MB
- **Python 3 + pip**: 50MB
- **System overhead (Alpine)**: 20-30MB
- **Base total**: ~150-200MB

### Chromium Browser (agent-browser)
- **Chromium base**: 200-400MB
- **Per tab**: 100-300MB additional
- **With JavaScript execution**: +50-200MB per page
- **Peak usage**: 500-800MB per browser session

### Total Memory Requirements
- **Minimum (no browser)**: 200MB
- **Light browser usage**: 512MB - 1GB
- **Normal browser usage**: 1GB - 2GB ✅ **Recommended**
- **Heavy browser usage**: 2GB - 4GB

---

## 🎯 Optimization Strategies

### 1. Chromium Launch Flags (Most Effective)

Set these environment variables to reduce Chromium memory usage:

```bash
# Essential flags for Cloud Run / containerized environments
CHROME_FLAGS="--disable-dev-shm-usage --no-sandbox --disable-setuid-sandbox --disable-gpu"

# Additional memory-saving flags
CHROME_FLAGS="$CHROME_FLAGS --disable-software-rasterizer --disable-extensions"
CHROME_FLAGS="$CHROME_FLAGS --disable-background-networking --disable-sync"
CHROME_FLAGS="$CHROME_FLAGS --disable-translate --disable-breakpad"
CHROME_FLAGS="$CHROME_FLAGS --disable-background-timer-throttling"
CHROME_FLAGS="$CHROME_FLAGS --disable-backgrounding-occluded-windows"
CHROME_FLAGS="$CHROME_FLAGS --disable-renderer-backgrounding"
CHROME_FLAGS="$CHROME_FLAGS --metrics-recording-only --mute-audio"

# Use new headless mode (lighter than old headless)
PLAYWRIGHT_CHROMIUM_USE_HEADLESS_NEW=1
```

**Memory Savings**: 100-200MB reduction

#### Flag Explanations

| Flag | Purpose | Memory Saved |
|------|---------|--------------|
| `--disable-dev-shm-usage` | Use `/tmp` instead of `/dev/shm` (critical for Cloud Run) | ~50-100MB |
| `--no-sandbox` | Disable sandboxing (container already isolated) | ~20-50MB |
| `--disable-gpu` | Disable GPU acceleration | ~30-80MB |
| `--disable-extensions` | No extension loading | ~10-20MB |
| `--disable-background-networking` | No background network requests | ~5-10MB |
| `--disable-sync` | No Chrome sync | ~5-10MB |
| `--disable-translate` | No translation service | ~5-10MB |

### 2. Browser Session Management

**Close browser when not in use:**
```bash
# After completing browser tasks
agent-browser close
```

**Reuse browser sessions:**
Instead of opening/closing frequently, keep one session and navigate:
```bash
agent-browser open https://example.com
agent-browser snapshot -i
# Do work...
agent-browser open https://another-site.com  # Reuses same browser instance
```

**Memory Savings**: 200-400MB per closed session

### 3. Limit Concurrent Operations

Configure in `~/.picoclaw/config.json`:

```json
{
  "agents": {
    "defaults": {
      "max_tool_iterations": 15,  // Reduced from 20
      "max_parallel_tools": 1      // Prevent multiple browser sessions
    }
  }
}
```

### 4. Use Regular Picoclaw for Non-Browser Tasks

If your task doesn't need browser automation, use the regular picoclaw image:

```yaml
# docker-compose.yml
services:
  picoclaw-gateway:
    image: sipeed/picoclaw:latest  # Regular image (40MB, <100MB RAM)
    # vs
    # dockerfile: docker/Dockerfile.heavy  # Heavy image (1.92GB, ~2GB RAM)
```

### 5. Cloud Run Specific Optimizations

#### Configure Memory Limits

```typescript
// infrastructure/index.ts
resources: {
    limits: {
        cpu: "2",           // More CPU helps Chromium render faster
        memory: "2048Mi",   // 2GB for comfortable browser automation
    },
    cpuIdle: true,          // Scale to zero when idle (save costs)
},
```

#### Adjust Scaling

```typescript
scaling: {
    minInstanceCount: 0,    // Scale to zero when idle
    maxInstanceCount: 3,    // Limit concurrent instances
},
```

#### Add /dev/shm Volume (Optional)

If not using `--disable-dev-shm-usage`:

```typescript
volumeMounts: [
    {
        name: "dshm",
        mountPath: "/dev/shm",
    },
],
// ...
volumes: [
    {
        name: "dshm",
        emptyDir: {
            medium: "Memory",
            sizeLimit: "512Mi",
        },
    },
],
```

---

## 🔢 Memory Allocation Guide

### Cloud Run Memory Recommendations

| Use Case | Memory | CPU | Notes |
|----------|--------|-----|-------|
| **No browser** | 512Mi | 1 | Regular picoclaw image |
| **Light browser** (1-2 simple pages) | 1024Mi | 1 | Basic automation |
| **Normal browser** (multiple pages) | 2048Mi | 2 | ✅ **Recommended** |
| **Heavy browser** (complex SPAs, multiple tabs) | 4096Mi | 2-4 | Large scale automation |

### Cost Considerations (GCP us-central1)

| Memory | vCPU | Cost per hour | Cost per 1M requests (avg 10s) |
|--------|------|---------------|--------------------------------|
| 512Mi  | 1    | $0.024        | $66.67                         |
| 1024Mi | 1    | $0.048        | $133.33                        |
| 2048Mi | 2    | $0.144        | $400.00                        |
| 4096Mi | 4    | $0.576        | $1,600.00                      |

> With `cpuIdle: true` and `minInstanceCount: 0`, you only pay when the service is actively handling requests.

---

## 🧪 Testing Your Configuration

### 1. Local Testing

```bash
# Build the heavy image
docker build -f docker/Dockerfile.heavy -t picoclaw-heavy:test .

# Run with memory limit
docker run --memory=2g --cpus=2 \
  -e CHROME_FLAGS="--disable-dev-shm-usage --no-sandbox --disable-gpu" \
  -e PLAYWRIGHT_CHROMIUM_USE_HEADLESS_NEW=1 \
  picoclaw-heavy:test

# Monitor memory usage
docker stats
```

### 2. Cloud Run Testing

```bash
# Deploy with new configuration
pulumi up

# Monitor logs
gcloud run services logs read picoclaw-gateway --region=asia-southeast1

# Check memory usage in Cloud Run metrics
gcloud monitoring read --filter="resource.type=cloud_run_revision" \
  --project=YOUR_PROJECT
```

### 3. Load Testing

```bash
# Test browser automation under load
for i in {1..10}; do
  curl -X POST https://your-service.run.app/api/chat \
    -H "Content-Type: application/json" \
    -d '{"message": "Open https://example.com and take a screenshot"}'
done

# Watch for OOM kills
gcloud run services logs read picoclaw-gateway --region=asia-southeast1 | grep -i "memory\|oom\|killed"
```

---

## 🚨 Troubleshooting

### OOM (Out of Memory) Errors

**Symptoms:**
```
Error: Failed to launch browser: spawn Unknown system error -12
Error: page.goto: Navigation timeout of 30000 ms exceeded
Container terminated: memory limit exceeded
```

**Solutions:**
1. ✅ Increase memory to 2GB or 4GB
2. ✅ Add `--disable-dev-shm-usage` flag
3. ✅ Close browser after each session
4. ✅ Reduce concurrent operations

### Slow Browser Performance

**Symptoms:**
- Timeouts on page loads
- Commands taking >60s

**Solutions:**
1. ✅ Increase CPU to 2 or 4 cores
2. ✅ Add `--disable-gpu` flag
3. ✅ Increase timeout in tool calls
4. ✅ Use `networkidle` wait less frequently

### High Costs

**Solutions:**
1. ✅ Use `minInstanceCount: 0` to scale to zero
2. ✅ Reduce `maxInstanceCount` to limit concurrent instances
3. ✅ Use regular picoclaw image for non-browser tasks
4. ✅ Implement request queuing to reuse instances

---

## 📋 Quick Setup Checklist

For Cloud Run deployment with browser automation:

- [ ] Set memory to **2048Mi** (2GB)
- [ ] Set CPU to **2 cores**
- [ ] Add `CHROME_FLAGS` environment variable
- [ ] Add `PLAYWRIGHT_CHROMIUM_USE_HEADLESS_NEW=1`
- [ ] Enable `cpuIdle: true` for cost savings
- [ ] Set `minInstanceCount: 0` to scale to zero
- [ ] Test with actual browser automation workload
- [ ] Monitor memory usage in Cloud Run console
- [ ] Set up alerts for OOM errors

---

## 📚 Additional Resources

- [Playwright Docker Guide](https://playwright.dev/docs/docker)
- [Chrome Headless Flags](https://peter.sh/experiments/chromium-command-line-switches/)
- [GCP Cloud Run Memory Settings](https://cloud.google.com/run/docs/configuring/memory-limits)
- [agent-browser Documentation](https://agent-browser.dev)

---

## 🎯 Recommended Configuration

For most production use cases with browser automation:

```typescript
// infrastructure/index.ts
resources: {
    limits: {
        cpu: "2",
        memory: "2048Mi",  // 2GB
    },
    cpuIdle: true,
},
envs: [
    { 
        name: "CHROME_FLAGS", 
        value: "--disable-dev-shm-usage --no-sandbox --disable-gpu --disable-software-rasterizer --disable-extensions --disable-background-networking --disable-sync --metrics-recording-only --mute-audio"
    },
    { 
        name: "PLAYWRIGHT_CHROMIUM_USE_HEADLESS_NEW", 
        value: "1" 
    },
    // ... other env vars
],
```

This provides a good balance between:
- ✅ Reliable browser automation
- ✅ Reasonable costs (~$0.144/hour active time)
- ✅ Good performance for most web pages
- ✅ Room for memory spikes
