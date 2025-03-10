package util

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	clusterV1alpha1 "github.com/tmax-cloud/hypercloud-multi-operator/apis/cluster/v1alpha1"
)

func Delete(namespace, cluster string) error {
	// hypercloud api call
	url := "https://hypercloud5-api-server-service.hypercloud5-system.svc.cluster.local/namespaces/{namespace}/clustermanagers/{clustermanager}"

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	url = strings.Replace(url, "{namespace}", namespace, -1)
	url = strings.Replace(url, "{clustermanager}", cluster, -1)
	client := &http.Client{Transport: tr}
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		log.Fatalf("An Error Occurred %v", err)
		return err
	}
	_, err = client.Do(req)

	// _, err := client.Post(url, "application/json", nil)
	if err != nil {
		log.Fatalf("An Error Occurred %v", err)
		return err
	}
	return nil
}

func Insert(clusterManager *clusterV1alpha1.ClusterManager) error {
	// hypercloud api call
	url := "https://hypercloud5-api-server-service.hypercloud5-system.svc.cluster.local/namespaces/{namespace}/clustermanagers/{clustermanager}"

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	// http.
	url = strings.Replace(url, "{namespace}", clusterManager.Namespace, -1)
	url = strings.Replace(url, "{clustermanager}", clusterManager.Name, -1)
	client := &http.Client{Transport: tr}

	// person := Person{"Alex", 10}
	data, _ := json.Marshal(clusterManager)
	buff := bytes.NewBuffer(data)
	_, err := client.Post(url, "application/json", buff)

	if err != nil {
		log.Fatalf("An Error Occurred %v", err)
		return err
	}
	return nil
}
