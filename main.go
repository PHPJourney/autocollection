package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/araddon/dateparse"
	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/fatih/color"
	fastping "github.com/tatsushid/go-fastping"
	"golang.org/x/crypto/sha3"
	"io/ioutil"
	"log"
	"math/big"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	pollInterval    = 5 * time.Second
	maxRetryDelay   = 3 * time.Second
	retryMultiplier = 2
)

var (
	connected      bool
	retryBaseDelay = 2 * time.Second
	connMutex      sync.RWMutex
	connChange     = make(chan struct{}, 1)
)

// 假设我们已经有了 ERC-721 ABI 的字节表示
var erc721ABI, _ = abi.JSON(strings.NewReader(`[{"anonymous":false,"inputs":[{"indexed":true,"name":"from","type":"address"},{"indexed":true,"name":"to","type":"address"},{"indexed":true,"name":"tokenId","type":"uint256"}],"name":"Transfer","type":"event"}]`))
var erc1155ABI, _ = abi.JSON(strings.NewReader(`[
    {
        "anonymous": false,
        "inputs": [
            {"indexed": true, "name": "operator", "type": "address"},
            {"indexed": true, "name": "from", "type": "address"},
            {"indexed": true, "name": "to", "type": "address"},
            {"indexed": false, "name": "id", "type": "uint256"},
            {"indexed": false, "name": "value", "type": "uint256"}
        ],
        "name": "TransferSingle",
        "type": "event"
    },
    {
        "anonymous": false,
        "inputs": [
            {"indexed": true, "name": "operator", "type": "address"},
            {"indexed": true, "name": "from", "type": "address"},
            {"indexed": true, "name": "to", "type": "address"},
            {"indexed": false, "name": "ids", "type": "uint256[]"},
            {"indexed": false, "name": "values", "type": "uint256[]"}
        ],
        "name": "TransferBatch",
        "type": "event"
    }
]
`))

// 定义 Transfer 事件的结构体
type Transfer struct {
	From    common.Address
	To      common.Address
	TokenId *big.Int
}

type PostPayload struct {
	Title         string      `json:"title"`
	Content       string      `json:"content"`
	Alias         string      `json:"slug"`
	Meta          interface{} `json:"meta"`
	Excerpt       string      `json:"excerpt"`
	Author        int         `json:"author"`
	Categorize    []int       `json:"categorize"`
	Format        string      `json:"format"`
	Tag           []string    `json:"tag"`
	FeaturedMedia int         `json:"featured_media,omitempty"`
	Status        string      `json:"status"`
}

type TransferSingle struct {
	Operator common.Address
	From     common.Address
	To       common.Address
	ID       *big.Int
	Value    *big.Int
}

type TransferBatch struct {
	Operator common.Address
	From     common.Address
	To       common.Address
	IDs      []*big.Int
	Values   []*big.Int
}

type MyTime struct {
	time.Time
}

type Image struct {
	ID           int          `json:"id"`
	Date         MyTime       `json:"date"`
	DateGmt      MyTime       `json:"date_gmt"`
	GUID         GUID         `json:"guid"`
	Link         string       `json:"link"`
	MediaDetails MediaDetails `json:"media_details"`
	SourceURL    string       // 附加字段，存储原始图片URL，通常从media_details中获取
}

type GUID struct {
	Rendered string `json:"rendered"`
}

type MediaDetails struct {
	File  string                  `json:"file"`
	Sizes map[string]WPImageSizes `json:"sizes"`
}
type WPImageSizes struct {
	SourceUrl string `json:"source_url"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	MimeType  string `json:"mime-type"`
}

// 实际使用时，可以扩展更多的字段，比如标题、描述、作者等信息

type Config struct {
	Network []Item     `json:"network"`
	Site    []SiteItem `json:"site"`
}
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

type Api struct {
	Categories string `json:"categories"`
	Mapping    map[string]struct {
		From int `json:"from"`
		To   int `json:"to"`
		ID   int `json:"id"`
	} `json:"mapping"`
}

type Media struct {
	Url    string `json:"url"`
	Method string `json:"method"`
}

type WordPressCategory struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	ID          int    `json:"id"`
	Description string `json:"description"`
	Parent      int    `json:"parent,omitempty"`
}

type Mapping struct {
	Title      string `json:"title"`
	Content    string `json:"content"`
	Author     string `json:"author"`
	Alias      string `json:"slug"`
	Categorize string `json:"categorize"`
	Format     string `json:"format"`
	Tag        string `json:"tag"`
	Excerpt    string `json:"excerpt"`
	Meta       string `json:"meta"`
	Status     string `json:"status"`
}

type Item struct {
	Host      string `json:"host"`
	Browser   string `json:"browser"`
	FromBlock int    `json:"from_block"`
	ChainId   int    `json:"chainId"`
	Name      string `json:"name"`
	Step      int    `json:"step"`
}
type TokenData struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Image       string `json:"image"`
	ExternalURL string `json:"external_url"`
	Attributes  interface {
	} `json:"attributes"`
	Properties struct {
	} `json:"properties"`
	Links struct {
	}
}

var logger *log.Logger = log.New(color.Output, "", 1)
var config Config
var configLock sync.Mutex
var clockInterval time.Time

func handleSignal(cancel context.CancelFunc) {
	fmt.Printf(color.BlueString("Caught signal,shutting down...\n"))
	cancel()
}

func main() {

	connected = true
	go networkMonitor()
	ctx, cancel := context.WithCancel(context.Background())
	// 创建一个channel，用于接收信号通知
	sigChan := make(chan os.Signal, 1)
	// 注册信号处理器，将信号发送到sigChan signal:interrupt
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGTSTP)
	logger.Println(color.GreenString("Starting service..."))
	data, err := ioutil.ReadFile("config.json")
	if err != nil {
		log.Fatal(err)
	}
	err = json.Unmarshal(data, &config)
	if err != nil {
		log.Print(err)
	}
	var wg sync.WaitGroup
	var rpchost string
	var from *big.Int
	var to *big.Int

	erc721Transfer := getEventSignature("Transfer(address,address,uint256)")
	erc1155Transfer := getEventSignature("TransferSingle(address,address,address,uint256,uint256)")
	erc1155Batch := getEventSignature("TransferBatch(address,address,address,uint256[],uint256[])")
	for _, item := range config.Network {
		connMutex.RLock()
		connected := connected
		connMutex.RUnlock()
		wg.Add(1)
		go func(ctx context.Context, network Item, site []SiteItem) {
			var mode bool
			mode = false
			defer wg.Done()
			log.Printf(color.GreenString("Start process by %s(ChainId: %d)"), network.Name, network.ChainId)
			rpchost = network.Host
			client, err := ethclient.Dial(rpchost)
			if err != nil {
				log.Println(err, network.ChainId)
			}
			var number *big.Int
			for {
				number = getNumber(network.ChainId, client)
				if number == nil {
					log.Println(err, network.Name, network.ChainId)
					time.Sleep(2 * time.Second)
				} else {
					break
				}
			}
			logger.Println(color.YellowString("latest BlockNumber:"), network.Name, network.ChainId, number)

			for i := network.FromBlock; i < int(number.Int64()); i++ {

				select {
				case <-ctx.Done(): // 当接收到信号时，context会被取消，此处监测到Done信号
					fmt.Println(color.BlueString("Received signal, exiting..."))
					handleSignal(cancel)
					return
				default:
					if !mode {
						from = big.NewInt(int64(i)).Mul(big.NewInt(int64(network.Step)), big.NewInt(int64(i)))
						to = big.NewInt(int64(i+1)).Mul(big.NewInt(int64(network.Step)), big.NewInt(int64(i+1)))
					} else {
						from = to
						to = from.Add(from, big.NewInt(1))
					}
					if to.Int64() >= number.Int64() {
						to = number
					}
					if from.Int64() >= number.Int64() {
						mode = true
						from = from.Sub(from, big.NewInt(int64(network.Step)))
						to = from.Add(from, big.NewInt(1))
					}
					if connected {
						updateConfig(network, to)
						logs, err := getEventLogs(
							client,
							network.Name,
							network.ChainId,
							from,
							to,
							721,
						)
						if err != nil {
							log.Print(err, network.ChainId)
						}
						logger.Println(color.YellowString("Block rand:"), from, to)
						log.Printf("getEventLogs finish: url - %v name - %s chainid - %d from - %s , to - %s type - %d\n",
							rpchost,
							network.Name,
							network.ChainId,
							from,
							to,
							721,
						)
						var logsData []types.Log
						for _, logItem := range logs {
							txlog, err := json.Marshal(logItem)
							if err != nil {
								logger.Println(color.RedString("Error:"), err, network.Name, network.ChainId, "json.Marshal failed")
								continue
							}
							c := color.New(color.FgCyan).Add(color.Underline).Add(color.FgRed).Add(color.FgYellow)
							c.Add(color.FgHiCyan)
							logger.Println(color.CyanString("Nft721 Tx:"), c.Sprint(string(txlog)), color.GreenString("Rand:"+from.String()+" - "+to.String()+"  ("+fmt.Sprintf("Current Height: %d", logItem.BlockNumber)+")"))
							transferEvent, err := parseTransferEvent(logItem)
							if err != nil {
								logger.Println(color.RedString("Error:"), err, network.Name, network.ChainId, "parseTransferEvent failed")
								continue
							} else if transferEvent != nil {
								logger.Println(color.GreenString("Info:"), transferEvent, network.Name, network.ChainId, "transferEvent")
								//return transferEvent, nil
							}
							// 假设NFT数据结构中前32字节是标识符（ID），接下来20字节是所有者地址
							if transferEvent.TokenId == nil { // 数据长度至少应该为52字节
								logger.Println(color.RedString("Error:"), "Nft721 Data parse failed")
								continue
							} else {
								logsData = append(logsData, logItem)
							}
						}
						erclogs, err := getEventLogs(
							client,
							network.Name,
							network.ChainId,
							from,
							to,
							1155,
						)
						if err != nil {
							log.Print(err, network.ChainId, from, to)
							continue
						}
						logsJson, err := json.Marshal(erclogs)
						if err != nil {
							logger.Println(color.RedString("Error:"), err, network.Name, network.ChainId, from, to, "json.Marshal failed")
							continue
						}
						logger.Println(color.CyanString("Nft1155 Logs:"), string(logsJson), from, to)
						for _, logItem := range erclogs {

							txlog, err := json.Marshal(logItem)
							if err != nil {
								logger.Println(color.RedString("Error:"), err, network.Name, network.ChainId, "json.Marshal failed")
								continue
							}

							c := color.New(color.FgCyan).Add(color.Underline).Add(color.FgRed).Add(color.FgYellow)
							c.Add(color.FgHiCyan)
							logger.Println(color.CyanString("Nft1155 Tx:"), c.Sprint(string(txlog)), from, to)
							logger.Println(color.GreenString("Topics Info:"), logItem.Topics[0].Hex(), erc1155Transfer.String(), erc1155Batch.String(), "erc1155Transfer", from, to)
							switch logItem.Topics[0].Hex() {
							case erc1155Transfer.String():
								transferEvent, err := parseTransferSingleEvent(logItem)
								if err != nil {
									logger.Println(color.RedString("Error:"), err, network.Name, network.ChainId, "parseTransferSingleEvent failed", from, to)
									continue
								} else if transferEvent != nil {
									logger.Println(color.GreenString("Info:"), transferEvent, network.Name, network.ChainId, "transferSingleEvent", from, to)
									//return transferEvent, nil
								}
								// 假设NFT数据结构中前32字节是标识符（ID），接下来20字节是所有者地址
								if transferEvent.ID == nil { // 数据长度至少应该为52字节
									logger.Println(color.RedString("Error:"), "Nft1155 Data single parse failed", from, to)
									continue
								} else {
									logsData = append(logsData, logItem)
								}
							case erc1155Batch.String():
								transferBatchEvent, err := parseTransferBatchEvent(logItem)
								if err != nil {
									logger.Println(color.RedString("Error:"), err, network.Name, network.ChainId, from, to, "parseTransferBatchEvent failed")
									continue
								} else if transferBatchEvent != nil {
									logger.Println(color.GreenString("Info:"), transferBatchEvent, network.Name, network.ChainId, from, to, "transferBatchEvent")
									//return transferEvent, nil
									if len(transferBatchEvent.IDs) < 1 { // 数据长度至少应该为52字节
										logger.Println(color.RedString("Error:"), from, to, "Nft1155 Data Batch parse failed")
										continue
									} else {
										logsData = append(logsData, logItem)
									}
								}
							}

						}

						if len(logsData) > 0 {
							for _, logItem := range logsData {
								// 解析data字段
								//data := logItem.Data

								// 解析合约地址
								contractAddress := logItem.Address
								fmt.Println(contractAddress, from, to, "id - owner - contractAddress")
								for _, www := range site {
									meta, _ := json.Marshal(logItem)
									if err != nil {
										log.Print(err, network.ChainId)
									}
									logger.Println(color.BlueString("log Detail: ") + string(meta))
									var token interface{}
									var tokenID []*big.Int
									var tokenType string
									var alias string
									logger.Println(color.MagentaString("Topic Hex:"), logItem.Topics[0].String(), erc721Transfer.String(), erc1155Transfer.String(), erc1155Batch.String(), from, to, "erc721Transfer")
									switch logItem.Topics[0].Hex() {
									case erc721Transfer.String():
										token, err = parseTransferEvent(logItem)
										if err != nil {
											logger.Println(color.RedString("Error:"), err, network.Name, network.ChainId, from, to, "parseTransferEvent failed")
											continue
										} else if token != nil {
											logger.Println(color.GreenString("Info:"), token, network.Name, network.ChainId, from, to, "transferEvent")
											//return transferEvent, nil
										}
										fmt.Println("ERC721 Transfer - TokenID: ", token.(*Transfer).TokenId, from, to)
										fmt.Println("ERC721 Transfer - From: ", token.(*Transfer).From, from, to)
										tokenID = append(tokenID, token.(*Transfer).TokenId)
										alias = token.(*Transfer).From.String()
										tokenType = "ERC721"
										fmt.Printf("ERC721 Transfer - TokenID: %s, \n", tokenID, from, to)
									case erc1155Transfer.String():
										token, err = parseTransferSingleEvent(logItem)
										tokenID = append(tokenID, token.(*TransferSingle).ID)
										alias = token.(*TransferSingle).From.String()
										tokenType = "ERC1155"
										fmt.Printf("ERC1155 TransferSingle - TokenID: %v From: %s To: %s\n", tokenID, from, to)
									case erc1155Batch.String():
										token, err = parseTransferBatchEvent(logItem)
										tokenID = append(tokenID, token.(*TransferBatch).IDs...)
										alias = token.(*TransferBatch).From.String()
										tokenType = "ERC1155"
										fmt.Println("ERC1155 TransferBatch - Not handled in the example", from, to)
									}

									if err != nil {
										log.Printf("Log event parse failed: %v From: %s To: %s\n", err, from, to)
										continue
									}
									tokenData, err := getNFTData(client, contractAddress, tokenID, tokenType)
									if err != nil {
										log.Printf("NFT data parse failed: %v From: %s To: %s \n", err, from, to)
										continue
									}
									var tag []string
									var content string
									featuredMedia := 0
									for i, tokenDataItem := range tokenData {
										content += tokenDataItem.Name + "\n"
										if i == 1 {
											content += `<figure class="wp-block-gallery has-nested-images columns-default is-cropped wp-block-gallery-1 is-layout-flex wp-block-gallery-is-layout-flex">`
										}
										if i == 0 {
											if tokenDataItem.Image != "" {
												logger.Println(color.GreenString("\nInfo:"), "Image URL:", tokenDataItem.Image)
												images := pullImageUrl(www, tokenDataItem.Image)
												if images != nil {
													featuredMedia = images.ID
													srcset := ""
													for _, sizeInfo := range images.MediaDetails.Sizes {
														srcset += fmt.Sprintf("%s %dw,", sizeInfo.SourceUrl, sizeInfo.Width)
													}
													if len(srcset) >= 2 {
														srcset = srcset[:len(srcset)-2]
													} else {
														srcset = ""
													}
													content += fmt.Sprintf(`<figure class="wp-block-image size-full"><img decoding="async" fetchpriority="high" width="512" height="512" src="%s" alt="%s" class="wp-image-%d" srcset="%s" sizes="(max-width: 512px) 100vw, 512px"></figure>`, images.SourceURL, images.Link, images.ID, srcset) + "\n"
												}
											}
										} else {
											if tokenDataItem.Image != "" {
												logger.Println(color.GreenString("\nInfo:"), "Image URL:", tokenDataItem.Image)
												images := pullImageUrl(www, tokenDataItem.Image)
												if images != nil {
													srcset := ""
													for _, sizeInfo := range images.MediaDetails.Sizes {
														srcset += fmt.Sprintf("%s %dw,", sizeInfo.SourceUrl, sizeInfo.Width)
													}
													srcset = srcset[:len(srcset)-2]
													content += fmt.Sprintf(`<figure class="wp-block-image size-large is-style-default"><img decoding="async" loading="lazy" width="512" height="512" data-id="%d" src="%s" alt="%s" class="wp-image-%d" srcset="%s"></figure>`, images.ID, images.SourceURL, tokenDataItem.Name, images.ID, srcset) + "\n"
												}
											}
										}
										tag = append(tag, tokenDataItem.Name)
										content += "</figure>"
										content += tokenDataItem.Description + "\n"
										content += fmt.Sprintf(`<a href="%s">%v</a>`, network.Browser+"/tx/"+logItem.TxHash.String(), tokenDataItem.Name+" By TxID")
									}

									if featuredMedia == 0 {
										continue
									}
									meta, err = json.Marshal(tokenData)
									if err != nil {
										logger.Print(color.RedString("Error:"), err, network.ChainId, from, to)
									}
									//tagStr, err := json.Marshal(tag)
									//if err != nil {
									//	logger.Print(color.RedString("Error:"), err, item.ChainId, from, to)
									//}

									article := PostPayload{
										Title:         "From " + contractAddress.String(),
										Content:       content,
										Meta:          tokenData,
										Alias:         network.Name + "-" + alias + "-" + contractAddress.String(),
										Categorize:    getCategorize(www, getBlockTime(client, logItem.TxHash.String()), contractAddress.String(), network),
										Format:        www.Format,
										Tag:           tag,
										Excerpt:       network.Browser + "/tx/" + logItem.TxHash.String(),
										Author:        www.Author,
										Status:        www.Status,
										FeaturedMedia: featuredMedia,
									}
									payload, err := json.Marshal(article)
									if err != nil {
										log.Print(err, network.ChainId)
									}

									c := color.New(color.FgCyan).Add(color.Underline).Add(color.FgRed).Add(color.FgYellow)
									c.Add(color.FgHiCyan)
									fmt.Println(c.Sprint(string(payload)), "article jsonData", network.ChainId, from, to)
									pushArticle(payload, www, network)
									break
								}
								fmt.Println(logItem, network.ChainId, from, to)
							}
						}
						if to.Int64() >= number.Int64() {
							for {
								number = getNumber(network.ChainId, client)
								if number != nil {
									break
								}
							}
						}
						fmt.Println()
						fmt.Printf("sleep %v second\n", 1*time.Second)
						time.Sleep(1 * time.Second)
					} else {
						fmt.Printf("Network disconnected, retrying processing of item '%s - %s' in %v\n", from, to, retryBaseDelay)
					}
					fmt.Printf("sleep %v Second\n", retryBaseDelay)
					time.Sleep(retryBaseDelay)
					//retryBaseDelay *= retryMultiplier
					if retryBaseDelay > maxRetryDelay {
						retryBaseDelay = maxRetryDelay
						fmt.Printf("update retryBaseDelay: %v\n", retryBaseDelay)
						fmt.Println()
					}
				}
			}
		}(ctx, item, config.Site)
	}
	// 开始监听信号
	sig := <-sigChan
	// 最终清理工作
	defer saveConfig() // 保存配置
	fmt.Printf(color.BlueString("Save configuration...\n"))
	fmt.Printf(color.BlueString("aught signal: %v, shutting down...\n"), sig)
	wg.Wait()
	// 关闭信号通知，防止在清理过程中重复接收信号
	signal.Stop(sigChan)
	fmt.Printf(color.YellowString("aught signal: %v, Stop signal notify... \n"), sig)
	// 等待清理工作完成
	<-ctx.Done()
	fmt.Println("Exiting gracefully.")
	fmt.Println("All threads have finished")
	os.Exit(0)
}

func saveConfig() {

	jsonData, err := json.MarshalIndent(config, "", "  ")
	fmt.Println(jsonData, "saveConfig json ")
	if err != nil {
		log.Printf("Marshal failed: %v", err)
	}
	err = ioutil.WriteFile("config.json", jsonData, 0644)
	if err != nil {
		log.Printf("Write file failed: %v", err)
	}
	//os.Exit(0)
}

func networkMonitor() {
	var lastState bool
	for {
		fmt.Println()
		state, err := getNetworkState()
		if err != nil {
			log.Printf("Error getting network state: %v Sleep %v Second", err, pollInterval)
			time.Sleep(pollInterval)
			continue
		}
		fmt.Printf(color.YellowString("Network state: %v\n"), state)
		if state != lastState {
			lastState = state
			log.Printf("Network state changed to %v\n", state)
			updateConnectedState(state)
			connChange <- struct{}{}
		}
		log.Printf("Recheck network and sleep %v Second\n", pollInterval)
		fmt.Println()
		time.Sleep(pollInterval)
	}
}

func getNetworkState() (bool, error) {
	//ips := make(map[string]string)
	// 定义一个通用的外部目标地址用于测试连通性
	//targetAddress := "8.8.8.8"
	var ipState = false
	pinger := fastping.NewPinger()
	//defer pinger.Stop()
	pingState := false
	interfaces, err := net.Interfaces()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting network interfaces: %v\n", err)
		return pingState, err
	}

	for _, i := range interfaces {
		if i.Flags&net.FlagUp == 0 {
			continue // 排除未激活的接口
		}
		// 打印网络接口的基本信息
		fmt.Printf("Interface Name: %s\n", i.Name)
		fmt.Printf("Flags:          %s\n", flagsToString(i.Flags))
		fmt.Printf("Hardware Addr:  %s\n", i.HardwareAddr.String())

		addresses, err := i.Addrs()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting addresses for interface %s: %v\n", i.Name, err)
			continue
		}
		for _, addr := range addresses {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			default:
				fmt.Fprintf(os.Stderr, "Unknown address type: %T\n", v)
				continue
			}
			// 根据您的需求，可以筛选出特定版本的IP地址（如IPv4或IPv6）
			if ip.IsGlobalUnicast() && ip.To4() != nil { // IPv4地址
				fmt.Printf("IPv4 Address:   %s\n", ip.String())
				src := &net.IPAddr{IP: ip, Zone: i.Name}
				pinger.AddIPAddr(src)
				ipState = true
				break
			}
		}
		if ipState {
			ipState = false
			break
		}
		fmt.Println() // 分割不同接口的输出
	}
	pinger.Network("ip4:icmp")
	pinger.OnRecv = func(addr *net.IPAddr, rtt time.Duration) {
		pingState = true
		fmt.Printf("Interface %s (IP: %s) is connected with RTT: %v\n", addr.Zone, addr.IP.String(), rtt)
		pinger.Stop()
	}
	pinger.OnIdle = func() {
		fmt.Println("All pings have been sent")
		fmt.Println()
		pinger.Stop()
	}
	err = pinger.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running pinger: %v\n", err)
	}
	return pingState, nil
}

func flagsToString(flags net.Flags) string {
	var s []string
	if flags*net.FlagBroadcast > 0 {
		s = append(s, "broadcast")
	}
	if flags&net.FlagLoopback > 0 {
		s = append(s, "loopback")
	}
	if flags&net.FlagPointToPoint > 0 {
		s = append(s, "point-to-point")
	}
	if flags&net.FlagUp > 0 {
		s = append(s, "up")
	}
	if flags&net.FlagMulticast > 0 {
		s = append(s, "multicast")
	}
	if flags&net.FlagRunning > 0 {
		s = append(s, "routing")
	}
	return strings.Join(s, "|")
}

func updateConnectedState(state bool) {
	connMutex.Lock()
	defer connMutex.Unlock()
	connected = state
	if connected {
		fmt.Println("Network connected")
	} else {
		fmt.Println("Network disconnected")
	}
}

func updateConfig(item Item, block *big.Int) {
	configLock.Lock()
	defer configLock.Unlock()

	found := false
	for i, network := range config.Network {
		if item.ChainId == network.ChainId {
			found = true
			newFromBlock := int(block.Int64()) / item.Step
			config.Network[i].FromBlock = newFromBlock
			log.Printf("Updated FromBlock for chain ID %d to %d", item.ChainId, newFromBlock)
			break
		}
	}
	if !found {
		log.Printf("Chain ID %d not found in config", item.ChainId)
	}
	if time.Duration(time.Now().Unix()-clockInterval.Unix()) > 300*time.Second || clockInterval.IsZero() {
		clockInterval = time.Now()
		updatedJson, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			log.Printf("Marshal failed: %v", err)
		}

		if err := ioutil.WriteFile("config.json", updatedJson, 0644); err != nil {
			log.Printf("Write file failed: %v", err)
		}
		log.Println("Configuration file has been updated successfully.")
		//os.Exit(0)
	}
}

func getBlockTime(client *ethclient.Client, hash string) time.Time {
	tx, _, err := client.TransactionByHash(context.Background(), common.HexToHash(hash))
	if err != nil {
		log.Println(color.RedString(err.Error()), "BlockByHash This website categories request failed", common.HexToHash(hash))
		return time.Now()
	}
	timestamp := tx.Time()
	formattedTime := time.Unix(timestamp.Unix(), 0).Format(time.RFC3339)
	t, err := time.Parse(time.RFC3339, formattedTime)
	if err != nil {
		log.Println(color.RedString(err.Error()), "time.Parse This website categories request failed")
		return time.Now()
	} else {
		return t
	}
}

func getCategorize(item SiteItem, createdTime time.Time, contractAddress string, network Item) []int {
	var categorizeIds []int
	hour := createdTime.Hour()
	cycleHour := hour % 12
	if cycleHour == 0 {
		cycleHour = 12
	}
	// 计算时辰
	chineseHourId := chineseTimeZones(cycleHour, item)
	// 读取分类
	req, err := http.NewRequest("GET", item.Api.Categories, nil)
	if err != nil {
		log.Println(color.RedString(err.Error()), "GetApi Categories This website categories request failed")
		return nil
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Println(color.RedString(err.Error()), "getCategorize client Do This website categories request failed")
		return nil
	}
	defer resp.Body.Close()
	var categories []WordPressCategory
	err = json.NewDecoder(resp.Body).Decode(&categories)
	if err != nil {
		log.Println(color.RedString(err.Error()), "getCategorize json.Decode This website categories decode failed")
		return nil
	}
	var netFlag bool = false
	var flag bool = false
	categorizeIds = append(categorizeIds, chineseHourId)
	for _, items := range categories {
		if items.Name == network.Name {
			netFlag = true
			categorizeIds = append(categorizeIds, items.ID)
		}
		if items.Name == contractAddress {
			flag = true
			categorizeIds = append(categorizeIds, items.ID)
		}
	}
	if !netFlag {
		newCategorie := newCategories(chineseHourId, network.Name, network.Name+" Network", item)
		categorizeIds = append(categorizeIds, newCategorie)
		newCategorie = newCategories(newCategorie, contractAddress, network.Name+" Contract:"+contractAddress, item)
		categorizeIds = append(categorizeIds, newCategorie)
	}
	if !flag {
		newCategorie := newCategories(chineseHourId, contractAddress, network.Name+" Contract:"+contractAddress, item)
		categorizeIds = append(categorizeIds, newCategorie)
	}
	return categorizeIds
}

func newCategories(chineseHourId int, name string, description string, item SiteItem) int {
	category := WordPressCategory{
		Name:        name,
		Description: description,
		Parent:      chineseHourId,
	}
	jsonData, err := json.Marshal(category)
	if err != nil {
		log.Println(color.RedString(err.Error()), "newCategories json.Marshal This website categories request failed")
		return 0
	}
	req, err := http.NewRequest("POST", item.Api.Categories, bytes.NewBuffer(jsonData))

	auth := item.Username + ":" + item.Token
	authEncodeed := base64.StdEncoding.EncodeToString([]byte(auth))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+authEncodeed)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Println(color.RedString(err.Error()), "newCategories client.Do This website categories request failed")
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		var newCategoriesId int
		err = json.NewDecoder(resp.Body).Decode(&newCategoriesId)
		if err != nil {
			log.Println(color.RedString(err.Error()), "newCategories json.Decode This website categories decode failed")
			return 0
		}
		return newCategoriesId
	} else {
		return 0
	}
}

func chineseTimeZones(hour int, item SiteItem) int {

	var ids int

	for _, items := range item.Api.Mapping {
		if items.From >= hour && items.To <= hour {
			ids = items.ID
			break
		}
	}
	return ids
}

func (m *MyTime) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || string(data) == `""` {
		return nil
	}
	timeStr := string(data)
	clearTimeStr := timeStr[1 : len(timeStr)-1]
	// Fractional seconds are handled implicitly by Parse.
	tt, err := dateparse.ParseAny(clearTimeStr)
	*m = MyTime{tt}
	return err
}

func pullImageUrl(www SiteItem, url string) *Image {
	if strings.HasPrefix(url, "ipfs://") {
		url = strings.Replace(url, "ipfs://", "https://ipfs.io/ipfs/", 1)
	} else if strings.HasPrefix(url, "https://") {
		fmt.Println("https is normal")
	} else if strings.HasPrefix(url, "http://") {
		fmt.Println("http is normal")
	} else if strings.HasPrefix(url, "ipns://") {
		url = strings.Replace(url, "ipns://", "https://ipfs.io/ipns/", 1)
	}
	resp, err := http.Get(url)
	if err != nil {
		log.Print(err, www.Url, "http.Get failed")
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		log.Print("http get failed", string(bodyBytes))
		return nil
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println(color.RedString("pullImageUrl failed:"), err)
		//os.Exit(0)
		return nil
	}
	// 创建 HTTP 客户端
	client := &http.Client{}

	// 构建 multipart/form-data 请求
	reqbody := &bytes.Buffer{}
	writer := multipart.NewWriter(reqbody)
	part, err := writer.CreateFormFile("file", "image.png")
	if err != nil {
		fmt.Println(color.RedString("Failed to create form file"), err)
		//os.Exit(0)
		return nil
	}
	_, err = part.Write(body)
	if err != nil {
		fmt.Println(color.RedString("Failed to write form file"), err)
		//os.Exit(0)
		return nil
	}
	writer.Close()

	req, err := http.NewRequest(www.Media.Method, www.Media.Url, reqbody)
	if err != nil {
		fmt.Println(color.RedString("Failed to create request"), reqbody, www.Media.Url, err)
		//os.Exit(0)
		return nil
	}
	auth := www.Username + ":" + www.Token
	authEncodeed := base64.StdEncoding.EncodeToString([]byte(auth))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Basic "+authEncodeed)

	resBuffer, err := client.Do(req)
	if err != nil {
		fmt.Println(color.RedString("Failed to send request"), err)
		return nil
	}
	defer resBuffer.Body.Close()
	if resBuffer.StatusCode != http.StatusCreated {
		response, _ := ioutil.ReadAll(resBuffer.Body)
		logger.Printf(color.RedString(`pullImageURL Response Status: %s Error Body: %s`), resBuffer.Status, string(response))
		return nil
	}
	// 解析响应
	response, err := ioutil.ReadAll(resBuffer.Body)
	if err != nil {
		logger.Println(color.RedString("Failed to read response"), err)
		return nil
	}
	logger.Println(color.YellowString("pullImageURL Response Status:"), resBuffer.Status, www.Name)
	logger.Println(color.YellowString("pullImageURL Response Body:"), string(response))
	var images Image
	err = json.Unmarshal(response, &images)
	if err != nil {
		logger.Println(color.RedString("Error:"), err)
		return nil
	}
	fmt.Println(&images, color.YellowString("images 上传完成结果"))
	return &images
}

func pushArticle(payload []byte, www SiteItem, item Item) {
	req, err := http.NewRequest(www.Method, www.Url, bytes.NewBuffer(payload))
	if err != nil {
		log.Printf("Error creating request: %v Name:%s", err, item.Name)
		return
	}
	logger.Println(color.YellowString("Request Body:"), string(payload), www.Token, www.Username, www.Url, www.Method)
	auth := www.Username + ":" + www.Token
	authEncodeed := base64.StdEncoding.EncodeToString([]byte(auth))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+authEncodeed)

	httpclient := &http.Client{}
	resp, err := httpclient.Do(req)
	if err != nil {
		log.Printf("Error sending request: %v", err)
		return
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading response body: %v", err)
		return
	}
	c := color.New(color.Underline)
	c.Add(color.FgYellow)
	log.Printf("pushArticle Response Body: %s", c.Sprint(body))
	log.Printf("pushArticle Response Status: %s", c.Sprint(resp.Status))
}

func getNumber(chainid int, client *ethclient.Client) *big.Int {
	if chainid == 10 || chainid == 42161 {
		block, err := client.HeaderByNumber(context.Background(), nil)
		if err != nil {
			log.Println(err, "getNumber")
			return nil
		}
		//blockC, err := json.Marshal(block)
		//log.Fatalln(string(blockC), block.Number)
		return block.Number
		//var number *big.Int
		//number = big.NewInt(block.Number())
		//return number
		return nil
	} else {
		block, err := client.BlockByNumber(context.Background(), nil)
		if err != nil {
			log.Println(err, "getNumber")
			return nil
		}
		return block.Number()
	}
}

func getEventSignature(functionName string) common.Hash {
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte(functionName))
	eventSignature := hash.Sum(nil)
	return common.BytesToHash(eventSignature)
}

func getEventLogs(client *ethclient.Client, name string, chainId int, from *big.Int, to *big.Int, nftType int) ([]types.Log, error) {
	var topics [][]common.Hash
	if nftType == 721 {
		topic := getEventSignature("Transfer(address,address,uint256)")
		topics = [][]common.Hash{
			{
				topic,
			},
		}
		log.Printf("topics: %s %s(%d)", topic, name, chainId)
	} else {
		topic1 := getEventSignature("TransferSingle(address,address,address,uint256,uint256)")
		topic2 := getEventSignature("TransferBatch(address,address,address,uint256[],uint256[])")
		topics = [][]common.Hash{
			{topic1},
			{topic2},
		}
	}
	query := ethereum.FilterQuery{
		FromBlock: from,
		ToBlock:   to,
		Topics:    topics,
	}
	meta, err := json.Marshal(query)
	if err != nil {
		log.Print(err, "json marshal query failed")
		return nil, err
	}
	logger.Println(color.CyanString("Log:"), string(meta), name, chainId, from, to, query, "getEventLogs")

	logs, err := client.FilterLogs(context.Background(), query)
	if err != nil {
		log.Println(err, name, chainId, query, "FilterLogs failed")
		return nil, err
	}
	return logs, err
}

func parseTransferSingleEvent(logs types.Log) (*TransferSingle, error) {
	var transferSingleEvent TransferSingle
	if err := erc1155ABI.UnpackIntoInterface(&transferSingleEvent, "TransferSingle", logs.Data); err != nil {
		log.Println(err, "Unpack failed")
		return nil, fmt.Errorf("failed to unpack TransferSingle event: %v", err)
	}

	operator := common.HexToAddress(logs.Topics[1].Hex())
	from := common.HexToAddress(logs.Topics[2].Hex())
	to := common.HexToAddress(logs.Topics[3].Hex())
	transferSingleEvent.Operator = operator
	transferSingleEvent.From = from
	transferSingleEvent.To = to
	return &transferSingleEvent, nil
}

func parseTransferBatchEvent(logs types.Log) (*TransferBatch, error) {
	var transferBatch TransferBatch
	if err := erc1155ABI.UnpackIntoInterface(&transferBatch, "TransferBatch", logs.Data); err != nil {
		log.Println(err, "UnpackIntoInterface failed")
		return nil, fmt.Errorf("failed to unpack TransferBatch event: %v", err)
	}
	operator := common.HexToAddress(logs.Topics[1].Hex())
	from := common.HexToAddress(logs.Topics[2].Hex())
	to := common.HexToAddress(logs.Topics[3].Hex())

	transferBatch.Operator = operator
	transferBatch.From = from
	transferBatch.To = to
	return &transferBatch, nil
}

func parseTransferEvent(logs types.Log) (*Transfer, error) {
	transferEvent := new(Transfer)
	err := erc721ABI.UnpackIntoInterface(transferEvent, "Transfer", logs.Data)
	if err != nil {
		log.Println(err, "UnpackIntoInterface failed")
		return nil, fmt.Errorf("failed to unpack Transfer event: %v", err)
	}
	if len(logs.Topics) < 4 {
		return nil, fmt.Errorf("failed to parse Transfer event: %v", err)
	}
	from := common.BytesToAddress(logs.Topics[1].Bytes())
	//if err != nil {
	//	log.Println(err, "DecodeAddress failed")
	//	return nil, fmt.Errorf("failed to decode 'from' address: %v", err)
	//}
	to := common.BytesToAddress(logs.Topics[2].Bytes())
	//to, err := hexutil.DecodeAddress(logs.Topics[2].Hex())
	//if err != nil {
	//	log.Println(err, "DecodeAddress failed")
	//	return nil, fmt.Errorf("failed to decode 'to' address: %v", err)
	//}
	tokenID := big.NewInt(0).SetBytes(logs.Topics[3].Bytes())
	transferEvent.From = from
	transferEvent.To = to
	transferEvent.TokenId = tokenID
	jsonToken, err := json.Marshal(transferEvent)
	if err != nil {
		log.Print(err)
	}
	log.Println("ERC721 Transfer - Token parse data: ", string(jsonToken))
	return transferEvent, nil
}

func getNFTData(client *ethclient.Client, contractAddr common.Address, tokenID []*big.Int, tokenType string) ([]*TokenData, error) {
	log.Printf("getNFTData params - contractAddr: %s, tokenID: %v\n", contractAddr, tokenID)
	// Call ERC721 tokenURI function to get URI
	var ABI abi.ABI
	var err error
	switch tokenType {
	case "ERC721":
		ABI, err = abi.JSON(strings.NewReader(`[{"constant":true,"inputs":[{"name":"tokenId","type":"uint256"}],"name":"tokenURI","outputs":[{"name":"","type":"string"}],"payable":false,"stateMutability":"view","type":"function"}]`))
		if err != nil {
			return nil, err
		}
	case "ERC1155":
		ABI, err = abi.JSON(strings.NewReader(`[{"constant":true,"inputs":[{"name":"_tokenId","type":"uint256"}],"name":"tokenURI","outputs":[{"name":"","type":"string","internalType":"string"}],"payable":false,"stateMutability":"view","type":"function"}]`))
		if err != nil {
			return nil, err
		}
	}
	log.Printf("getNFTData abi: %+v %+v", ABI, ABI.Methods)
	var tokenData []*TokenData
	for _, tokenID := range tokenID {
		data, err := ABI.Pack("tokenURI", tokenID)
		if err != nil {
			return nil, err
		}

		msg := ethereum.CallMsg{
			To:   &contractAddr,
			Data: data,
		}

		tokenURIBytes, err := client.CallContract(context.Background(), msg, nil)
		if err != nil {
			return nil, err
		}

		tokenURIByString := string(tokenURIBytes)
		if tokenURIByString == "" {
			return nil, fmt.Errorf("failed to get tokenURI")
		}

		// 查找"http://"或"https://"的索引位置
		httpIndex := strings.Index(tokenURIByString, "http://")
		httpsIndex := strings.Index(tokenURIByString, "https://")
		ipfsIndex := strings.Index(tokenURIByString, "ipfs://")
		ipnsIndex := strings.Index(tokenURIByString, "ipns://")
		base64Index := strings.Index(tokenURIByString, "data:application/json;base64,")
		// 计算并选择最小的非负索引值作为偏移量
		offsetLength := len(tokenURIByString)
		if httpIndex >= 0 {
			offsetLength = httpIndex
		} else if httpsIndex >= 0 {
			offsetLength = httpsIndex
		} else if ipfsIndex >= 0 {
			offsetLength = ipfsIndex
		} else if ipnsIndex >= 0 {
			offsetLength = ipnsIndex
		} else if base64Index >= 0 {
			offsetLength = base64Index
		}
		tokenURI := tokenURIByString[offsetLength:]
		log.Printf("Original tokenURI: %s", tokenURI)
		log.Printf("Trimmed tokenURI: %s", tokenURI)
		log.Printf("getNFTData tokenURI: %s", tokenURI)
		if strings.HasPrefix(tokenURI, "ipfs://") {
			tokenURI = strings.Replace(tokenURI, "ipfs://", "https://ipfs.io/ipfs/", 1)
		} else if strings.HasPrefix(tokenURI, "http://") {
			fmt.Println("http is normal")
		} else if strings.HasPrefix(tokenURI, "https://") {
			fmt.Println("https is normal")
		} else if strings.HasPrefix(tokenURI, "ipns://") {
			tokenURI = strings.Replace(tokenURI, "ipns://", "https://ipfs.io/ipns/", 1)
		} else if strings.HasPrefix(tokenURI, "data:application/json;base64,") {
			parts := strings.Split(tokenURI, ",")
			//contentType := parts[0][7:] // remove "data:"
			encodedJson := parts[1]
			decodedJson, err := base64.StdEncoding.DecodeString(encodedJson)
			if err != nil {
				return nil, err
			}
			var nftData TokenData
			err = json.Unmarshal(decodedJson, &nftData)
			if err == nil {
				tokenData = append(tokenData, &nftData)
				continue
			} else {
				str := string(decodedJson)
				if isValidURL(str) {
					tokenURI = str
				} else {
					return nil, fmt.Errorf("Could not parse the JSON content: %s", err.Error())
				}
			}
		} else {
			return nil, fmt.Errorf("failed to get tokenURI")
		}
		// Call URI to get NFT data
		resp, err := http.Get(cleanUrl(tokenURI))
		if err != nil {
			log.Printf("getNFTData failed to get tokenURI: %s", err.Error())
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := ioutil.ReadAll(resp.Body)
			return nil, fmt.Errorf("failed to get tokenURI: %s Response body: %s", resp.Status, string(bodyBytes))
		}
		var datas interface{}
		bodyBytes, _ := ioutil.ReadAll(resp.Body)
		err = json.Unmarshal(bodyBytes, &datas)
		if err != nil {
			fmt.Println(color.RedString("Error:getNFTData Body is not valid JSON."))
			return nil, err
		} else {
			fmt.Println("getNFTData Body is valid JSON.", string(bodyBytes))
		}
		var tokenInfo TokenData
		err = json.Unmarshal(bodyBytes, &tokenInfo)
		if err != nil {
			log.Printf("getNFTData data parse failed: %s, Response body: %s", err, string(bodyBytes))
			return nil, err
		}
		fmt.Printf("getNFTData tokenInfo: %+v", tokenInfo)
		tokenData = append(tokenData, &tokenInfo)
	}
	return tokenData, nil
}

func isValidURL(str string) bool {
	u, err := url.Parse(str)
	return err == nil && u.Scheme != "" && u.Host != ""
}

func cleanUrl(urlStr string) string {
	return strings.TrimRightFunc(urlStr, func(r rune) bool {
		return r <= 31 || r == 127
	})
}
