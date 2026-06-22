The values are compacted differently depending on how the name of the metric ends:

Metrics:
- `*_min` (float) : Minimum, we keep the minimum value of the metrics 
- `*_max` (float) : Maximum, we keep the maximum value of the metrics
- `*_avg` (float) : Average, we keep the average value of the metrics
- `*_pct` (float) : Percentage, we keep the percentage value of the metric
- `*_rte` (float) : Rate, we keep the rate value of the metric
- `*_sum` (float) : Sum, we keep the sum of the metric
- `*_cnt` (int) : Count, we keep the sum count of the metrics
- `*_val` (string): Values, we keep the string values with their counts (example: {"200": 100, "404": 2})
- `*` (int): Count, we keep the count of the metric
- `*` (float): Average, we keep the average value of the metric
- `*` (string): Values, we keep the string values with their counts (example: {"200": 100, "404": 2})
