param([string]$BaseUrl = 'http://localhost:8081')
$ErrorActionPreference = 'Stop'
Write-Host 'Catalogue (second request should return X-Cache: HIT)'
Invoke-WebRequest "$BaseUrl/products" -UseBasicParsing | Select-Object StatusCode,Headers
Invoke-WebRequest "$BaseUrl/products" -UseBasicParsing | Select-Object StatusCode,Headers
$taskKey = [guid]::NewGuid().ToString('N')
$taskBody = @{ customer = 'Demo Customer'; items = @(@{ product_id = 1; quantity = 2 }, @{ product_id = 2; quantity = 1 }) } | ConvertTo-Json -Depth 5
$taskHeaders = @{ 'Idempotency-Key' = $taskKey }
$taskOrder = Invoke-RestMethod "$BaseUrl/orders" -Method Post -Headers $taskHeaders -ContentType 'application/json' -Body $taskBody
Write-Host 'Created order'
$taskOrder | ConvertTo-Json -Depth 5
Write-Host 'Replay: same order ID'
$taskReplay = Invoke-RestMethod "$BaseUrl/orders" -Method Post -Headers $taskHeaders -ContentType 'application/json' -Body $taskBody
if ($taskOrder.id -ne $taskReplay.id) { throw 'Idempotency failed' }
Invoke-RestMethod "$BaseUrl/products/1/stock"
Write-Host 'Cancel twice: stock must be restored once'
Invoke-RestMethod "$BaseUrl/orders/$($taskOrder.id)/cancel" -Method Post | ConvertTo-Json -Depth 5
Invoke-RestMethod "$BaseUrl/orders/$($taskOrder.id)/cancel" -Method Post | ConvertTo-Json -Depth 5
Invoke-RestMethod "$BaseUrl/products/1/stock"
