cd .
Push-Location services\gateway; go build -o ..\..\bin\openfield-gateway.exe .\cmd; Pop-Location
Push-Location services\account; go build -o ..\..\bin\openfield-account.exe .\cmd; Pop-Location
Push-Location services\storage; go build -o ..\..\bin\openfield-storage.exe .\cmd; Pop-Location
Push-Location services\chat;    go build -o ..\..\bin\openfield-chat.exe .\cmd;    Pop-Location
Push-Location services\posts;   go build -o ..\..\bin\openfield-posts.exe .\cmd;   Pop-Location