package memcached

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	// magic number，目前没啥鸟用
	SET_FLAGS          = 123456
	NOT_VALID_RESP_MSG = "无效的服务器响应数据"
)

type Memcached struct {
	conn Connection
}

func (m *Memcached) New(host string, port int) {
	m.conn = Connection{
		Host: host,
		Port: port,
	}
}

func (m *Memcached) checkError(resp string) error {
	if strings.Compare(resp, "ERROR\r\n") == 0 {
		return errors.New("发生错误：ERROR")
	}
	matched, _ := regexp.MatchString("CLIENT_ERROR .+\r\n", resp)
	if matched {
		return errors.New(fmt.Sprintf("发生错误：%s", strings.TrimRight(resp, "\r\n")))
	}
	matched, _ = regexp.MatchString("SERVER_ERROR .+\r\n", resp)
	if matched {
		return errors.New(fmt.Sprintf("发生错误：%s", strings.TrimRight(resp, "\r\n")))
	}
	return nil
}

func (m *Memcached) checkStorageCmdResp(resp string) error {
	ERRORS := map[string]error{
		"STORED\r\n":     nil,
		"NOT_STORED\r\n": NotStoredError("未能存储数据"),
		"EXISTS\r\n":     ExistsError("数据已存在/已被别人修改"),
		"NOT_FOUND\r\n":  NotFoundError("未找到对应的数据"),
	}

	for k, v := range ERRORS {
		if strings.Compare(resp, k) == 0 {
			return v
		}
	}

	return errors.New("未知错误：" + string(resp))
}

/*
存储类型命令：set、add、replace、append、prepend、cas
*/

type storageCmdArgStruct map[string]interface{}

func (m *Memcached) runStorageCmd(cmdName string, args storageCmdArgStruct) error {
	// 必须
	key, ok := args["key"]
	if ok == false {
		return errors.New("缺少参数key")
	} else {
		key = key.(string)
	}
	value, ok := args["value"]
	if ok == false {
		return errors.New("缺少参数value")
	} else {
		value = value.(string)
	}

	// 可选
	flags, ok := args["flags"]
	if ok == false {
		flags = strconv.Itoa(SET_FLAGS)
	} else {
		flags = string(flags.(int))
	}
	expTime, ok := args["expire_time"]
	if ok == false {
		expTime = "0"
	} else {
		expTime = string(expTime.(int))
	}
	argList := []string{key.(string), flags.(string), expTime.(string), strconv.Itoa(len(value.(string)))}
	if cmdName == "cas" {
		if casUnique, ok := args["cas_unique"]; ok {
			argList = append(argList, casUnique.(string))
		}
	}

	cmd := fmt.Sprintf("%s %s\r\n", cmdName, strings.Join(argList, " "))
	resp, err := m.conn.Send(cmd, fmt.Sprintf("%s\r\n", value))
	if err != nil {
		return err
	}
	respString := string(resp)
	err = m.checkError(respString)
	if err != nil {
		return err
	}
	return m.checkStorageCmdResp(respString)
}

func (m *Memcached) Set(args storageCmdArgStruct) error {
	return m.runStorageCmd("set", args)
}

func (m *Memcached) Add(args storageCmdArgStruct) error {
	return m.runStorageCmd("add", args)
}

func (m *Memcached) Replace(args storageCmdArgStruct) error {
	return m.runStorageCmd("replace", args)
}

func (m *Memcached) Append(args storageCmdArgStruct) error {
	return m.runStorageCmd("append", args)
}

func (m *Memcached) Prepend(args storageCmdArgStruct) error {
	return m.runStorageCmd("prepend", args)
}

func (m *Memcached) Cas(args storageCmdArgStruct) error {
	return m.runStorageCmd("cas", args)
}

/*
数据获取类型命令：get、gets
*/

func (m *Memcached) parseFetchResp(resp []byte) (map[string]string, error) {
	respLines := strings.Split(strings.TrimRight(string(resp), "\r\n"), "\r\n")
	lineCount := len(respLines)
	if lineCount < 2 {
		return nil, NotValidRespError(NOT_VALID_RESP_MSG)
	}
	lastLine := respLines[lineCount-1]
	if lastLine != "END" {
		return nil, NotValidRespError(NOT_VALID_RESP_MSG)
	}
	filteredRespLength := len(resp) - len("\r\nEND\r\n")
	// 类型 []byte
	filteredResp := resp[:filteredRespLength]
	parsedKV := make(map[string]string)
	lineBreakLength := len("\r\n")
	for len(filteredResp) > 0 {
		if !bytes.HasPrefix(filteredResp, []byte("VALUE ")) {
			return nil, NotValidRespError(NOT_VALID_RESP_MSG)
		}
		lineBreakPosition := bytes.Index(filteredResp, []byte("\r\n"))
		if lineBreakPosition == -1 {
			return nil, NotValidRespError(NOT_VALID_RESP_MSG)
		}
		itemMetaLine := filteredResp[:lineBreakPosition]
		metaLineParts := bytes.Split(itemMetaLine, []byte(" "))
		if len(metaLineParts) != 4 {
			return nil, NotValidRespError(NOT_VALID_RESP_MSG)
		}
		// 目标key
		targetKey := string(metaLineParts[1])

		dataBeginPosition := lineBreakPosition + lineBreakLength
		targetValueLength, _ := strconv.Atoi(string(metaLineParts[3]))
		dataEndPosition := dataBeginPosition + targetValueLength
		// 目标value
		targetValue := filteredResp[dataBeginPosition:dataEndPosition]
		parsedKV[targetKey] = string(targetValue)

		if dataEndPosition == len(filteredResp) {
			filteredResp = []byte{}
		} else {
			filteredResp = filteredResp[dataEndPosition+lineBreakLength:]
		}
	}
	return parsedKV, nil
}

func (m *Memcached) runFetchCmd(cmdName, keys string) (map[string]string, error) {
	cmd := fmt.Sprintf("%s %s\r\n", cmdName, keys)
	resp, err := m.conn.Send(cmd)
	if err != nil {
		return nil, err
	}
	err = m.checkError(string(resp))
	if err != nil {
		return nil, err
	}
	return m.parseFetchResp(resp)
}

func (m *Memcached) Get(key string) (string, error) {
	resp, err := m.runFetchCmd("get", key)
	if err != nil {
		return "", err
	}
	return resp[key], nil
}

func (m *Memcached) Gets(keys ...string) (map[string]string, error) {
	resp, err := m.runFetchCmd("gets", strings.Join(keys, " "))
	if err != nil {
		return nil, err
	}
	return resp, nil
}

/*
其他命令：flush_all、delete、incr、decr、touch、stats
*/

func (m *Memcached) FlushAll() bool {
	cmd := "flush_all\r\n"
	resp, err := m.conn.Send(cmd)
	if err != nil {
		fmt.Println(err)
		return false
	}
	fmt.Printf("response: %s\n", string(resp))
	return true
}

func (m *Memcached) Stats(args ...string) interface{} {
	var cmd string
	if len(args) == 0 {
		cmd = "stats\r\n"
	} else {
		cmd = fmt.Sprintf("stats %s\r\n", args[0])
	}
	resp, err := m.conn.Send(cmd)
	if err != nil {
		fmt.Println(err)
		return nil
	}
	fmt.Printf("response: %s\n", string(resp))
	return string(resp)
}

func (m *Memcached) Close() {
	err := m.conn.Conn.Close()
	if err != nil {
		fmt.Println(err)
	}
}
