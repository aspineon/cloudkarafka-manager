package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/84codes/cloudkarafka-mgmt/config"
	"github.com/84codes/cloudkarafka-mgmt/zookeeper"
)

type Partition struct {
	Leader          int   `json:"leader"`
	Size            int   `json:"size"`
	LogStartOffset  int   `json:"log_start_offset"`
	LogEndOffset    int   `json:"log_end_offset"`
	Replicas        []int `json:"replicas"`
	ISR             []int `json:"isr"`
	LeaderEpoch     int   `json:"leader_epoch"`
	Version         int   `json:"version"`
	ControllerEpoch int   `json:"controller_epoch"`
}

type TopicConfig struct {
	Version int                    `json:"version"`
	Data    map[string]interface{} `json:"config"`
}

func (t TopicConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.Data)
}

type TopicMetricValue struct {
	BrokerId int `json:"broker_id"`
	Value    int `json:"value"`
}

type TopicMetrics map[string][]TopicMetricValue

type Topic struct {
	Name       string
	Partitions []Partition
}

func (t Topic) Size() int {
	sum := 0
	for _, p := range t.Partitions {
		sum += p.Size
	}
	return sum
}
func (t Topic) Messages() int {
	sum := 0
	for _, p := range t.Partitions {
		sum += p.LogEndOffset - p.LogStartOffset
	}
	return sum
}

func (t Topic) MarshalJSON() ([]byte, error) {
	res := map[string]interface{}{
		"name":               t.Name,
		"partitions":         t.Partitions,
		"partition_count":    len(t.Partitions),
		"replication_factor": 0,
	}
	if len(t.Partitions) > 0 {
		res["replication_factor"] = len(t.Partitions[0].Replicas)
	}
	res["size"] = t.Size()
	res["message_count"] = t.Messages()
	return json.Marshal(res)
}

var EmptyTopic = Topic{}

func brokerIsLeader(partitions []string, partition string) bool {
	for _, r := range partitions {
		if r == partition {
			return true
		}
	}
	return false
}

func partitionMetrics(ctx context.Context, topic Topic) Topic {
	dist := make(map[int][]string)
	for num, p := range topic.Partitions {
		dist[p.Leader] = append(dist[p.Leader], strconv.Itoa(num))
	}
	metricNames := []string{"LogStartOffset", "LogEndOffset", "Size"}
	l := len(metricNames) * len(dist)
	ch := make(chan []Metric)
	for leader, _ := range dist {
		for _, metricName := range metricNames {
			bean := fmt.Sprintf("kafka.log:type=Log,name=%s,topic=%s,partition=*", metricName, topic.Name)
			go QueryBrokerAsync(leader, bean, "Value", ch)
		}
	}
	for i := 0; i < l; i++ {
		select {
		case ms := <-ch:
			for _, m := range ms {
				if brokerIsLeader(dist[m.Broker], m.Partition) {
					i, _ := strconv.Atoi(m.Partition)
					switch m.Name {
					case "LogStartOffset":
						topic.Partitions[i].LogStartOffset = int(m.Value)
					case "LogEndOffset":
						topic.Partitions[i].LogEndOffset = int(m.Value)
					case "Size":
						topic.Partitions[i].Size = int(m.Value)
					default:
						fmt.Fprintf(os.Stderr, "[INFO] Missing parser for metric %s\n", m.Name)
					}
				}
			}
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "[INFO] Partition metrics request timed out %s: %s\n", topic.Name, ctx.Err())
			return topic
		}
	}
	return topic
}

func FetchTopic(ctx context.Context, topicName string) (Topic, error) {
	topic, err := zookeeper.Topic(topicName)
	if err != nil {
		if err == zookeeper.PathDoesNotExistsErr {
			fmt.Fprintf(os.Stderr, "[INFO] FetchTopic: topic %s does not exists in zookeeper", topicName)
		} else {
			fmt.Fprintf(os.Stderr, "[INFO] FetchTopic: %s", err)
		}
		return EmptyTopic, err
	} else {
		t := Topic{
			Name:       topicName,
			Partitions: make([]Partition, len(topic.Partitions)),
		}
		for p, replicas := range topic.Partitions {
			var par Partition
			partitionPath := fmt.Sprintf("/brokers/topics/%s/partitions/%s/state", topicName, p)
			if err := zookeeper.Get(partitionPath, &par); err == nil {
				i, _ := strconv.Atoi(p)
				par.Replicas = replicas
				t.Partitions[i] = par
			}
		}
		t = partitionMetrics(ctx, t)
		return t, nil
	}
}

func FetchTopicList(ctx context.Context, p zookeeper.Permissions) ([]Topic, error) {
	topics, err := zookeeper.Topics(p)
	if err != nil {
		return nil, err
	}
	res := make([]Topic, len(topics))
	for i, topicName := range topics {
		topic, err := FetchTopic(ctx, topicName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[WARN] TopicList: %s", err)
			res[i] = EmptyTopic
		} else {
			res[i] = topic
		}
	}
	return res, nil
}

func FetchTopicConfig(ctx context.Context, topicName string) (TopicConfig, error) {
	var topicConfig TopicConfig
	err := zookeeper.Get(fmt.Sprintf("/config/topics/%s", topicName), &topicConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[WARN] TopicInfo get topic config for %s: %s", topicName, err)
	}
	return topicConfig, err
}

func FetchTopicMetrics(ctx context.Context, topicName string) (TopicMetrics, error) {
	metrics := make(TopicMetrics)
	metricNames := []string{"MessagesInPerSec", "BytesRejectedPerSec", "BytesOutPerSec", "BytesInPerSec"}
	query := "kafka.server:type=BrokerTopicMetrics,name=%s,topic=%s"
	l := len(config.BrokerUrls) * len(metricNames)
	ch := make(chan []Metric)
	for brokerId, _ := range config.BrokerUrls {
		for _, metricName := range metricNames {
			bean := fmt.Sprintf(query, metricName, topicName)
			go QueryBrokerAsync(brokerId, bean, "OneMinuteRate", ch)
		}
	}
	for i := 0; i < l; i++ {
		select {
		case ress := <-ch:
			for _, res := range ress {
				metrics[res.Name] = append(metrics[res.Name], TopicMetricValue{res.Broker, int(res.Value)})
			}
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "[INFO] Topic metrics request timed out: %s\n", ctx.Err())
			return metrics, RequestTimedOutErr
		}
	}
	return metrics, nil
}