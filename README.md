<center>

# 自动采集区块NFT信息

</center>

---

## 软件描述

1. 软件采用Golang进行编码开发
2. 主要使用库来自于 [Ethereum](https://github.com/ethereum/go-ethereum)
3. 本软件支持同时对多个链进行数据抓取，采用多线程进行业务处理
4. 当区块扫描到启动时最新块时，将再次从链上自动更新最新高度
5. 本软件仅提供NFT数据信息抓取，不对其他数据格式进行检索
6. 本软件为自用产品，如有其他需求定制，可以与软件作者联系
7. Developer Author：[Akastar](mailto:mindy.stanley566@dontsp.am) 
8. Date: 03-21-2024 20:13:32 

---
### 网络配置介绍
>>> JSON 根节点通过network开始
> 

### 节点结构
```go
type Item struct {
	Browser   string `json:"browser"`
    Host      string `json:"host"`
    FromBlock int    `json:"from_block"`
    ChainId   int    `json:"chainId"`
    Name      string `json:"name"`
    Step      int    `json:"step"`
}
```
- `browser` _区块浏览器地址_
- `name` _网络名称_
- `chainId` _链ID_
- `step` _步进长度_ *（每次遍历区块数量）*
- `host` _RPC服务器节点_ *（请注意看rpc节点返回错误信息，部分节点不支持读取历史日志信息）*
- `from_block` _开始区块高度_ *（为了防止做无效工作，浪费时间和降低工作效率，可以直接在一开始启动服务时就将高度设置到后面，直接抛弃前置无效数据）*

---
### 配置文件示例
```json5
{
"network": [
    {
      "browser": "https://etherscan.io",
      "name": "Ethereum",
      "chainId": 1,
      "step": 100,
      "host": "wss://mainnet.gateway.tenderly.co",
      "from_block": 4477
    },
    {
      "browser": "https://bscscan.com",
      "name": "Binance Smart chain",
      "chainId": 56,
      "step": 10,
      "host": "https://bsc-dataseed.binance.org/",
      "from_block": 1000
    }
  ]
}
```
---

### 备份站点

```json

    {
      "name": "Binance Smart chain",
      "chainId": 56,
      "step": 10,
      "host": "https://bsc-dataseed.binance.org",
      "browser": "https://bscscan.com",
      "from_block": 1000
    }
```
---
### 站点配置
>>> JSON 根节点通过site开始
```go
type SiteItem struct {
Name     string  `json:"name"`
Url      string  `json:"url"`
Username string  `json:"username"`
Token    string  `json:"token"`
Api      Api     `json:"api"`
Mapping  Mapping `json:"mapping"`
Format   string  `json:"format"`
Author   int     `json:"author"`
Method   string  `json:"method"`
Status   string  `json:"status"`
Media    Media   `json:"media"`
}
```
- `name` _站点名称_
- `url` _站点地址_
- `username` _登录用户名_
- `token` _登录密码_
- `api` _API接口_
- `mapping` _数据映射_
- `format` _数据格式_
- `author` _作者ID_
- `method` _请求方式_
- `status` _站点状态_
- `media` _媒体信息_

---

### 配置文件示例
```json5 
{
  "site": [
    {
      "name": "Your Site name",
      "url": "https://sample.com/wp-json/wp/v2/posts",
      "username": "Your site Username",
      "token": "Your Site Api Token",
      "api": {
        "categories": "https://sample/wp-json/wp/v2/categories",
        "mapping": {
          // Api分类映射
          "分类名称": {
            "from": 1,
            //开始时间（Hour）
            "to": 3,
            // 结束时间（Hour）
            "id": 7
            // 分类ID
          },
        }
      },
      "mapping": {
        // 字段映射
        "title": "title",
        "content": "content",
        "author": "author",
        "slug": "slug",
        "categorize": "categories",
        "format": "format",
        "tag": "tags",
        "excerpt": "excerpt",
        "meta": "meta",
        "status": "status"
      },
      "format": "gallery",
      // 图片格式
      "author": 1,
      // 作者ID
      "method": "POST",
      "status": "publish",
      // 文章发布状态
      "media": {
        "url": "https://sample.com/wp-json/wp/v2/media",
        // 媒体发布Api
        "method": "POST"
      }
    }
  ]
}
```

--- 

#### 完整配置示例
```json5
{
  "network": [
    // ...
  ],
  "site": [
    // ...
  ]
}
```