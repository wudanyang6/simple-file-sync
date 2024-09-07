# 使用说明

server 端参数
```Shell
-limit-dir string
    Limit directory, start with home dir
-port int
    Port to listen on (default 8120)
-token string
    Token for authentication
```

client 端参数
```Shell
-dir string
    Directory to watch (default "/path/to/your/watched/directory")
-mode string
    Initialization mode: 'all' to upload all files, 'git' to upload git diff files (default "all")
-target string
    Target directory on server (default "/path/to/your/destination/directory")
-url string
    Server URL (default "http://localhost:8080/receiver")
```

# 其他

因为是自己使用，用来将代码同步到开发机的工具，所以有一些硬编码的地方

1. 为了不把开发机搞坏，限制了文件只能传到当前启动用户的家目录下的某个目录
2. 在服务端也设置了一个参数，可以限制只能传到家目录的哪个目录
3. 客户端的 mode 是指初次启动客户端时的初始化，上传的文件