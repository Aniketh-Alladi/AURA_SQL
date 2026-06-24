-- 1. Create the schema
CREATE TABLE customers (id INT, name TEXT);
CREATE TABLE products (id INT, name TEXT, price INT);
CREATE TABLE orders (id INT, customer_id INT, product_id INT);

-- 2. Populate small dimension tables (The optimizer should join these first)
INSERT INTO customers VALUES (1, 'Alice');
INSERT INTO customers VALUES (2, 'Bob');
INSERT INTO customers VALUES (3, 'Charlie');

INSERT INTO products VALUES (101, 'Laptop', 1000);
INSERT INTO products VALUES (102, 'Mouse', 50);

-- 3. Populate the large fact table (50 rows)
-- In a real test, you'd loop this to 10,000+ rows
INSERT INTO orders VALUES (1, 1, 101);
INSERT INTO orders VALUES (2, 2, 102);
INSERT INTO orders VALUES (3, 3, 101);
-- ... repeat until you have enough data to slow down a bad join order