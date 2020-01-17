// Copyright 2015 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// A simple example exposing fictional RPC latencies with different types of
// random distributions (uniform, normal, and exponential) as Prometheus
// metrics.
package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"strconv"
	"strings"

	"log"
	//"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/d2r2/go-bsbmp"
	"github.com/d2r2/go-dht"
	"github.com/d2r2/go-i2c"

	//"gobot.io/x/gobot"
	//"gobot.io/x/gobot/drivers/i2c"
	//"gobot.io/x/gobot/platforms/firmata"

	//"github.com/morus12/dht22"
)

var (
	addr              = flag.String("listen-address", ":8080", "The address to listen on for HTTP requests.")
	uniformDomain     = flag.Float64("uniform.domain", 0.0002, "The domain for the uniform distribution.")
	normDomain        = flag.Float64("normal.domain", 0.0002, "The domain for the normal distribution.")
	normMean          = flag.Float64("normal.mean", 0.00001, "The mean for the normal distribution.")
	oscillationPeriod = flag.Duration("oscillation-period", 10*time.Minute, "The duration of the rate oscillation period.")
)

var (
	sensorsTemperature = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "rpi",
			Subsystem: "sensors",
			Name:      "temperature_c",
			Help:      "Temperature",
		},
		[]string{"pin"},
	)

	systemTemperature = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "rpi",
			Subsystem: "system",
			Name:      "temperature_c",
			Help:      "Temperature",
		},
		[]string{},
	)

	sensorsHumidity = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "rpi",
			Subsystem: "sensors",
			Name:      "humidity_percent",
			Help:      "Humidity",
		},
		[]string{"pin"},
	)

	sensorsTemperatureRaw = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "rpi",
			Subsystem: "sensors",
			Name:      "temperature_c_raw",
			Help:      "Temperature",
		},
		[]string{"pin"},
	)

	sensorsHumidityRaw = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "rpi",
			Subsystem: "sensors",
			Name:      "humidity_percent_raw",
			Help:      "Humidity",
		},
		[]string{"pin"},
	)

	sensorsFails = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "rpi",
			Subsystem: "sensors",
			Name: "fails",
			Help: "How many times Sensors fails.",
		},
		[]string{"type", "pin"},
	)

	sensorsPressurePa = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "rpi",
			Subsystem: "sensors",
			Name:      "pressure_kpa",
			Help:      "Atmospheric pressure in kilo-pascal",
		},
		[]string{"type"},
	)

	sensorsPressurePaRaw = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "rpi",
			Subsystem: "sensors",
			Name:      "pressure_kpa_raw",
			Help:      "Atmospheric pressure in kilo-pascal",
		},
		[]string{"type"},
	)

	sensorsPressureMMHg = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "rpi",
			Subsystem: "sensors",
			Name:      "pressure_mm_hg",
			Help:      "Atmospheric pressure in mmHg",
		},
		[]string{"type"},
	)

	sensorsAltitude = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "rpi",
			Subsystem: "sensors",
			Name:      "altitude",
			Help: "atmospheric altitude in meters above sea level," +
				" if we assume that pressure at see level is equal to 101325 Pa.",
		},
		[]string{"type"},
	)
)

func init() {
	// extra sensors
	prometheus.MustRegister(sensorsAltitude)
	prometheus.MustRegister(sensorsHumidity)
	prometheus.MustRegister(sensorsHumidityRaw)
	prometheus.MustRegister(sensorsPressureMMHg)
	prometheus.MustRegister(sensorsPressurePa)
	prometheus.MustRegister(sensorsPressurePaRaw)
	prometheus.MustRegister(sensorsTemperature)
	prometheus.MustRegister(sensorsTemperatureRaw)
	prometheus.MustRegister(sensorsFails)

	// TODO:
	// on board sensors (cpu load, temp, mem usage and etc)
	prometheus.MustRegister(systemTemperature)

	// Add Go module build info.
	prometheus.MustRegister(prometheus.NewBuildInfoCollector())
}

func validateHumidity(previousHumidity float32, newHumidity float32) bool {
	if math.Abs(float64(previousHumidity - newHumidity)) > 20 {
		return false
	}
	return newHumidity > 0
}

func validateTemperature(previousTemperature float32, newTemperature float32) bool {
	if math.Abs(float64(previousTemperature - newTemperature)) > 20 {
		return false
	}
	return newTemperature < 100
}

func validatePressure(previousPressure float32, newPressure float32) bool {
	if math.Abs(float64(previousPressure-newPressure)) > 1e4 {
		return false
	}
	return true
}

func checkError (e error) bool {
	if e != nil {
		fmt.Println("Error", e)
		return true
	}
	return false
}

func readCPUTemp () (float64, error) {
	dat, err := ioutil.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if checkError(err) {
		return 0, err
	}
	text := strings.TrimSpace(string(dat))
	temp, err := strconv.ParseFloat(text, 64)
	if checkError(err) {
		return 0, err
	}
	return temp / 1000.0, err
}


func main() {
	//logger.ChangePackageLogLevel("bsbmp", logger.InfoLevel)

	flag.Parse()

	// Create new connection to i2c-bus on 1 line with address 0x76.
	// Use i2cdetect utility to find device address over the i2c-bus
	i2c, err := i2c.NewI2C(0x77, 1)
	if err != nil {
		log.Fatal(err)
	}
	defer i2c.Close()
	// Uncomment next line to supress verbose output
	//logger.ChangePackageLogLevel("i2c", logger.InfoLevel)

	bmpSensor, err := bsbmp.NewBMP(bsbmp.BMP180, i2c)
	if err != nil {
		log.Fatal(err)
	}
	id, err := bmpSensor.ReadSensorID()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("This Bosch Sensortec bmpSensor has signature: 0x%x", id)

	smoothingFactor := 0.85

	dht_pins := []int{22, 23, 24}

	// TODO: prometheus gets measurements once in 15 seconds
	// so we can read values once in 3 second and rolling average
	// it could give us more precise value.
	// sure it doesn't solve bias error
	for _, pin := range dht_pins {
		go func(pin int) {
			sensorType := dht.DHT22
			pin_name := strconv.Itoa(pin)

			temperature, humidity, _, err :=
				dht.ReadDHTxxWithRetry(sensorType, pin, false, 10)
			if err != nil {
				fmt.Println("error", err)
			}
			previousTemperature := temperature
			previousHumidity := humidity
			avgTemperature := float64(temperature)
			avgHumidity := float64(humidity)
			log.Println("Start", pin_name)
			for {
				temperature, humidity, _, err :=
					dht.ReadDHTxxWithRetry(sensorType, pin, false, 10)
				if err != nil {
					fmt.Println("error", err)
				}
				log.Printf("Temperature = %v*C\n", temperature)
				log.Printf("Humidity = %v*%%\n", humidity)

				if validateHumidity(previousHumidity, humidity) && validateTemperature(previousTemperature, temperature) {
					avgTemperature = smoothingFactor*avgTemperature + (1-smoothingFactor)*float64(temperature)
					avgHumidity = smoothingFactor*avgHumidity + (1-smoothingFactor)*float64(humidity)
					sensorsTemperature.WithLabelValues(pin_name).Set(avgTemperature)
					sensorsHumidity.WithLabelValues(pin_name).Set(avgHumidity)
				} else {
					// sometimes sensors fails
					sensorsFails.WithLabelValues("dht", pin_name).Inc()
				}

				previousTemperature = temperature
				previousHumidity = humidity

				sensorsTemperatureRaw.WithLabelValues(pin_name).Set(float64(temperature))
				sensorsHumidityRaw.WithLabelValues(pin_name).Set(float64(humidity))

				time.Sleep(time.Duration(3 * time.Second))
			}
		}(pin)
	}

	go func() {
		for {
			temperature, err := readCPUTemp()
			if err != nil {
				sensorsFails.WithLabelValues("cpu", "temperature").Inc()
			} else {
				systemTemperature.WithLabelValues().Set(temperature)
			}

			time.Sleep(time.Duration(15 * time.Second))
		}
	}()

	go func() {
		sensorName := "bmp"
		log.Println("Start", sensorName)
		temperature, err := bmpSensor.ReadTemperatureC(bsbmp.ACCURACY_HIGH)
		if err != nil {
			log.Fatal(err)
		}
		avgTemperature := float64(temperature)
		previousTemperature := temperature

		p, err := bmpSensor.ReadPressurePa(bsbmp.ACCURACY_HIGH)
		if err != nil {
			log.Fatal(err)
		}
		previousPressurePa := p
		for {
			err = bmpSensor.IsValidCoefficients()
			if err != nil {
				sensorsFails.WithLabelValues(sensorName, "invalid_coefficients").Inc()
				log.Fatal(err)
			}

			temperature, err := bmpSensor.ReadTemperatureC(bsbmp.ACCURACY_HIGH)
			if err != nil {
				sensorsFails.WithLabelValues(sensorName, "temperature").Inc()
				log.Fatal(err)
			}
			log.Printf("Temperature = %v*C\n", temperature)

			if validateTemperature(previousTemperature, temperature) {
				avgTemperature = smoothingFactor*avgTemperature + (1-smoothingFactor)*float64(temperature)
				sensorsTemperature.WithLabelValues(sensorName).Set(avgTemperature)
			} else {
				// sometimes sensors fails
				sensorsFails.WithLabelValues(sensorName, "temperature").Inc()
			}

			sensorsTemperatureRaw.WithLabelValues(sensorName).Set(float64(temperature))
			previousTemperature = temperature

			// Read atmospheric pressure in pascal
			p, err := bmpSensor.ReadPressurePa(bsbmp.ACCURACY_HIGH)
			if err != nil {
				sensorsFails.WithLabelValues(sensorName, "pressure").Inc()
				log.Fatal(err)
			}
			log.Printf("Pressure = %v Pa\n", p)

			if validatePressure(previousPressurePa, p) {
				sensorsPressurePa.WithLabelValues(sensorName).Set(float64(p) / 1000)
			} else {
				sensorsFails.WithLabelValues(sensorName, "pressure").Inc()
			}

			sensorsPressurePaRaw.WithLabelValues(sensorName).Set(float64(p) / 1000)
			previousPressurePa = p

			// Read atmospheric pressure in mmHg
			p1, err := bmpSensor.ReadPressureMmHg(bsbmp.ACCURACY_HIGH)
			if err != nil {
				log.Fatal(err)
			}
			log.Printf("Pressure = %v mmHg\n", p1)

			sensorsPressureMMHg.WithLabelValues(sensorName).Set(float64(p1))

			// Read atmospheric altitude in meters above sea level, if we assume
			// that pressure at see level is equal to 101325 Pa.
			a, err := bmpSensor.ReadAltitude(bsbmp.ACCURACY_HIGH)
			if err != nil {
				log.Fatal(err)
			}
			log.Printf("Altitude = %v m\n", a)
			sensorsAltitude.WithLabelValues(sensorName).Set(float64(p1))

			time.Sleep(time.Duration(3 * time.Second))
		}
	}()

	// Expose the registered metrics via HTTP.
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
            <head><title>Monitoring RPI</title></head>
            <body>
            <h1>Monitoring RPI (GO)</h1>
            <p><a href='/metrics'>Metrics</a></p>
            </body>
            </html>`))
	})
	log.Fatal(http.ListenAndServe(*addr, nil))
}
