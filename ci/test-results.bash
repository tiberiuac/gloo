mkdir -p test_results

for f in $(find . -name "junit.xml" -print)
do
  echo "Moving $f"
  cp $f test_results
done