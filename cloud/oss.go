package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OSS 下载加速(架构: 云服务只签 URL 不碰图片字节, 见内部 oss-architecture 记忆)。
// 环境变量(由 systemd EnvironmentFile /etc/aiwallpaper/oss.env 注入, 严禁入库):
//   AW_OSS_AK_ID / AW_OSS_AK_SECRET / AW_OSS_BUCKET / AW_OSS_ENDPOINT(如 oss-cn-hangzhou.aliyuncs.com)
// 未配置 → 预签名禁用, 图片端点全部回退本地文件(现网行为不变)。
//
// 签名算法(V1 query 认证, 已对真实桶五连探针+预签名下载实证):
//   StringToSign = "GET\n\n\n" + Expires(纪元秒) + "\n" + "/" + bucket + "/" + key
//   Authorization 参数: OSSAccessKeyId / Expires / Signature(base64(hmac-sha1))

func ossEnv() (ak, secret, bucket, endpoint string) {
	return os.Getenv("AW_OSS_AK_ID"), os.Getenv("AW_OSS_AK_SECRET"),
		os.Getenv("AW_OSS_BUCKET"), os.Getenv("AW_OSS_ENDPOINT")
}

// ossConfigured 四件套齐全才算启用。
func ossConfigured() bool {
	ak, secret, bucket, endpoint := ossEnv()
	return ak != "" && secret != "" && bucket != "" && endpoint != ""
}

// presignOSSGET 生成对象 GET 预签名 URL(默认 15 分钟有效)。
func presignOSSGET(key string, expire time.Duration) (string, error) {
	return presignOSSGETAt(key, time.Now().Add(expire).Unix())
}

// presignOSSGETAt 纯函数版(供测试注入固定 Expires)。
func presignOSSGETAt(key string, expires int64) (string, error) {
	ak, secret, bucket, endpoint := ossEnv()
	if ak == "" || secret == "" || bucket == "" || endpoint == "" {
		return "", fmt.Errorf("OSS 未配置")
	}
	key = strings.TrimPrefix(key, "/")
	sts := "GET\n\n\n" + strconv.FormatInt(expires, 10) + "\n/" + bucket + "/" + key
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(sts))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	// 对象 key 按路径段转义(保留 /)
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return fmt.Sprintf("https://%s.%s/%s?OSSAccessKeyId=%s&Expires=%d&Signature=%s",
		bucket, endpoint, strings.Join(segs, "/"),
		url.QueryEscape(ak), expires, url.QueryEscape(sig)), nil
}

// ossUploadEnabled P1 出图机直传开关: AW_OSS_UPLOAD=1 且 OSS 四件套已配置。
// 关闭(默认)时队列走现网一体 Generate 路径, 行为零变化。
func ossUploadEnabled() bool {
	return os.Getenv("AW_OSS_UPLOAD") == "1" && ossConfigured()
}

// ---------- 对象管理操作(PUT/COPY/DELETE, V1 header 认证) ----------
// 与 presign 同源的签名: StringToSign 五段 join, x-oss-* 头参与签名(如 COPY 的 x-oss-copy-source)。

// ossSignHeader 生成 Date 与 Authorization(ossHeaders 仅收 x-oss-* 键)。
func ossSignHeader(method, resource, md5, ctype string, ossHeaders map[string]string) (date, auth string) {
	ak, secret, _, _ := ossEnv()
	date = time.Now().UTC().Format(http.TimeFormat)
	hdrs := ""
	if len(ossHeaders) > 0 {
		keys := make([]string, 0, len(ossHeaders))
		for k := range ossHeaders {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			hdrs += k + ":" + ossHeaders[k] + "\n"
		}
	}
	sts := method + "\n" + md5 + "\n" + ctype + "\n" + date + "\n" + hdrs + resource
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(sts))
	return date, "OSS " + ak + ":" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func ossObjectURL(key string) string {
	_, _, bucket, endpoint := ossEnv()
	segs := strings.Split(strings.TrimPrefix(key, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return fmt.Sprintf("https://%s.%s/%s", bucket, endpoint, strings.Join(segs, "/"))
}

// ossPutObject 上传对象(Content-MD5 由 OSS 校验, 通过=完整性保证)。
func ossPutObject(key string, data []byte, contentType string) error {
	_, _, bucket, _ := ossEnv()
	if !ossConfigured() {
		return fmt.Errorf("OSS 未配置")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	sum := md5.Sum(data)
	md5b64 := base64.StdEncoding.EncodeToString(sum[:])
	resource := "/" + bucket + "/" + key
	date, auth := ossSignHeader("PUT", resource, md5b64, contentType, nil)
	req, err := http.NewRequest(http.MethodPut, ossObjectURL(key), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Date", date)
	req.Header.Set("Content-MD5", md5b64)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("OSS PUT HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// ossCopyObject 服务端拷贝(同桶内重键, 不经过本地字节)。
func ossCopyObject(srcKey, dstKey string) error {
	_, _, bucket, _ := ossEnv()
	if !ossConfigured() {
		return fmt.Errorf("OSS 未配置")
	}
	resource := "/" + bucket + "/" + dstKey
	date, auth := ossSignHeader("PUT", resource, "", "", map[string]string{
		"x-oss-copy-source": "/" + bucket + "/" + srcKey,
	})
	req, err := http.NewRequest(http.MethodPut, ossObjectURL(dstKey), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Date", date)
	req.Header.Set("x-oss-copy-source", "/"+bucket+"/"+srcKey)
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("OSS COPY HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// ossDeleteObject 删除对象。
func ossDeleteObject(key string) error {
	_, _, bucket, _ := ossEnv()
	if !ossConfigured() {
		return fmt.Errorf("OSS 未配置")
	}
	resource := "/" + bucket + "/" + key
	date, auth := ossSignHeader("DELETE", resource, "", "", nil)
	req, err := http.NewRequest(http.MethodDelete, ossObjectURL(key), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Date", date)
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("OSS DELETE HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
