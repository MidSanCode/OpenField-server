::cd /d services\gateway && go build -o ..\..\bin\openfield-gateway.exe .\cmd && cd /d ..\..
::cd /d services\account && go build -o ..\..\bin\openfield-account.exe .\cmd && cd /d ..\..
::cd /d services\storage && go build -o ..\..\bin\openfield-storage.exe .\cmd && cd /d ..\..
cd /d services\chat && go build -o ..\..\bin\openfield-chat.exe .\cmd && cd /d ..\..
cd /d services\posts && go build -o ..\..\bin\openfield-posts.exe .\cmd && cd /d ..\..
pause