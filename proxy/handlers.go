package proxy

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	_ "github.com/jinzhu/gorm/dialects/mysql"
	"github.com/marfintack/argylepool/connector"
	"github.com/marfintack/argylepool/models"
	"github.com/sammy007/open-ethereum-pool/rpc"
	"github.com/sammy007/open-ethereum-pool/util"
)

// Allow only lowercase hexadecimal with 0x prefix
var noncePattern = regexp.MustCompile("^0x[0-9a-f]{16}$")
var hashPattern = regexp.MustCompile("^0x[0-9a-f]{64}$")
var workerPattern = regexp.MustCompile("^[0-9a-zA-Z-_]{1,8}$")

// Stratum
func (s *ProxyServer) handleLoginRPC(cs *Session, params []string, id string) (bool, *ErrorReply) {
	if len(params) == 0 {
		return false, &ErrorReply{Code: -1, Message: "Invalid params"}
	}

	login := strings.ToLower(params[0])
	if !util.IsValidHexAddress(login) {
		return false, &ErrorReply{Code: -1, Message: "Invalid login"}
	}
	if !s.policy.ApplyLoginPolicy(login, cs.ip) {
		return false, &ErrorReply{Code: -1, Message: "You are blacklisted"}
	}
	cs.login = login
	s.registerSession(cs)
	log.Printf("Stratum miner connected %v@%v", login, cs.ip)
	return true, nil
}

func (s *ProxyServer) handleGetWorkRPC(cs *Session) ([]string, *ErrorReply) {
	t := s.currentBlockTemplate()
	if t == nil || len(t.Header) == 0 || s.isSick() {
		return nil, &ErrorReply{Code: 0, Message: "Work not ready"}
	}
	return []string{t.Header, t.Seed, s.diff}, nil
}

// Stratum
func (s *ProxyServer) handleTCPSubmitRPC(cs *Session, id string, params []string) (bool, *ErrorReply) {
	s.sessionsMu.RLock()
	_, ok := s.sessions[cs]
	s.sessionsMu.RUnlock()

	if !ok {
		return false, &ErrorReply{Code: 25, Message: "Not subscribed"}
	}
	return s.handleSubmitRPC(cs, cs.login, id, params)
}

func (s *ProxyServer) handleSubmitRPC(cs *Session, login, id string, params []string) (bool, *ErrorReply) {
	// config := config.GetConfig()
	// dbURI := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=%s&parseTime=True",
	// 	config.DB.Username,
	// 	config.DB.Password,
	// 	config.DB.Host,
	// 	config.DB.Port,
	// 	config.DB.Name,
	// 	config.DB.Charset)

	if len(params) != 3 {
		s.policy.ApplyMalformedPolicy(cs.ip)
		log.Printf("Malformed params from %s@%s %v", login, cs.ip, params)
		return false, &ErrorReply{Code: -1, Message: "Invalid params"}
	}

	if !noncePattern.MatchString(params[0]) || !hashPattern.MatchString(params[1]) || !hashPattern.MatchString(params[2]) {
		s.policy.ApplyMalformedPolicy(cs.ip)
		log.Printf("Malformed PoW result from %s@%s %v", login, cs.ip, params)
		return false, &ErrorReply{Code: -1, Message: "Malformed PoW result"}
	}
	t := s.currentBlockTemplate()
	exist, validShare := s.processShare(login, id, cs.ip, t, params)
	ok := s.policy.ApplySharePolicy(cs.ip, !exist && validShare)

	if exist {
		log.Printf("Duplicate share from %s@%s %v", login, cs.ip, params)
		return false, &ErrorReply{Code: 22, Message: "Duplicate share"}
	}

	if !validShare {
		log.Printf("Invalid share from %s@%s", login, cs.ip)
		// Bad shares limit reached, return error and close
		if !ok {
			return false, &ErrorReply{Code: 23, Message: "Invalid share"}
		}
		return false, nil
	}
	log.Printf("Valid share from %s@%s", login, cs.ip)
	// db, err := gorm.Open(config.DB.Dialect, dbURI)
	// if err != nil {
	// 	log.Fatal("Could not connect database %s", err)
	// }
	db := connector.getConnection()
	Miner := models.MinerDetail{MinerAddress: login, BlockNumber: 75000, Reward: 5}
	db.Save(&Miner)
	if !workerPattern.MatchString(id) {
		id = "0"
	}
	minerRewardModel := models.MinerReward{}
	db.First(&minerRewardModel)
	reward := minerRewardModel.RewardValue
	log.Printf("Miner Reward is set to %s", reward)
	apiUrl := "https://admin.argylecoin.com"
	resource := "/transferTokenAdmin/"
	data := url.Values{}
	data.Set("tokens", reward)
	data.Add("toAddress", login)

	u, _ := url.ParseRequestURI(apiUrl)
	u.Path = resource
	urlStr := u.String() // 'https://api.com/user/'

	client := &http.Client{}
	newr, _ := http.NewRequest("POST", urlStr, strings.NewReader(data.Encode())) // URL-encoded payload
	newr.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	newr.Header.Add("Content-Length", strconv.Itoa(len(data.Encode())))
	response, err := client.Do(newr)
	if err != nil {
		fmt.Printf("The HTTP request failed with error %s\n", err)
	} else {
		data, _ := ioutil.ReadAll(response.Body)
		fmt.Println("Argle Transferred Successfully To the Miner")
		fmt.Println(string(data))

		//respondJSON(w, http.StatusOK, string(data))
	}
	if !ok {
		return true, &ErrorReply{Code: -1, Message: "High rate of invalid shares"}
	}
	return true, nil
}

func (s *ProxyServer) handleGetBlockByNumberRPC() *rpc.GetBlockReplyPart {
	t := s.currentBlockTemplate()
	var reply *rpc.GetBlockReplyPart
	if t != nil {
		reply = t.GetPendingBlockCache
	}
	return reply
}

func (s *ProxyServer) handleUnknownRPC(cs *Session, m string) *ErrorReply {
	log.Printf("Unknown request method %s from %s", m, cs.ip)
	s.policy.ApplyMalformedPolicy(cs.ip)
	return &ErrorReply{Code: -3, Message: "Method not found"}
}
